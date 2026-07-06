"""Adapter over the ``kind`` / ``docker`` / ``flux`` / ``git`` CLIs for local clusters.

This lives in the clients layer because it shells out to external tools. Its
:meth:`KindClient.create` encapsulates the canonical kind "local registry" recipe
(https://kind.sigs.k8s.io/docs/user/local-registry/): a ``registry:2`` container
pushable from ``localhost:<port>`` and reachable in-cluster as
``<registry-name>:5000``, so Flux ``OCIRepository`` sources can pull artifacts
pushed straight from the workstation.
"""

from __future__ import annotations

import subprocess

from dantofa.clients.local.errors import LocalClusterError

# The registry answers on :5000 inside its container; the host maps a chosen port
# to it, and in-cluster clients reach it by container name over the kind network.
_REGISTRY_CONTAINER_PORT = 5000
_KIND_NETWORK = "kind"

# One control-plane + three workers. containerdConfigPatches enables the
# per-registry config directory so the mirror hosts.toml the recipe drops on each
# node is honoured.
_KIND_CONFIG = """\
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry]
      config_path = "/etc/containerd/certs.d"
nodes:
  - role: control-plane
  - role: worker
  - role: worker
  - role: worker
"""


def _run(args: list[str], *, stdin: str | None = None) -> str:
    """Run a command, returning stdout; raise LocalClusterError on failure."""
    try:
        proc = subprocess.run(  # noqa: S603 - fixed argv (no shell); inputs are args
            args,
            input=stdin,
            capture_output=True,
            text=True,
            check=False,
        )
    except FileNotFoundError as exc:
        raise LocalClusterError(
            f"{args[0]!r} is not installed or not on PATH."
        ) from exc
    if proc.returncode != 0:
        detail = proc.stderr.strip() or proc.stdout.strip()
        raise LocalClusterError(f"`{' '.join(args)}` failed:\n{detail}")
    return proc.stdout


def _query(args: list[str]) -> str | None:
    """Run a read-only command, returning stdout or None if it fails."""
    try:
        return _run(args)
    except LocalClusterError:
        return None


class KindClient:
    """Semantic wrapper over kind (+ docker/kubectl) for local dev clusters."""

    def list(self) -> list[str]:
        return [line for line in _run(["kind", "get", "clusters"]).splitlines() if line]

    def get_kubeconfig(self, name: str) -> str:
        return _run(["kind", "get", "kubeconfig", "--name", name])

    def git_provenance(self) -> tuple[str, str]:
        """Return ``(source, revision)`` describing the working tree for the OCI stamp."""
        source = (_query(["git", "config", "--get", "remote.origin.url"]) or "").strip()
        if not source:
            toplevel = (_query(["git", "rev-parse", "--show-toplevel"]) or ".").strip()
            source = f"file://{toplevel}"
        branch = (
            _query(["git", "rev-parse", "--abbrev-ref", "HEAD"]) or "HEAD"
        ).strip()
        commit = _run(["git", "rev-parse", "HEAD"]).strip()
        revision = f"{branch}@sha1:{commit}"
        if (_query(["git", "status", "--porcelain"]) or "").strip():
            revision += "-dirty"
        return source, revision

    def push_artifact(self, url: str, *, path: str, source: str, revision: str) -> None:
        _ = _run(
            [
                "flux",
                "push",
                "artifact",
                url,
                "--path",
                path,
                "--source",
                source,
                "--revision",
                revision,
            ]
        )

    def reconcile_source(self, name: str) -> None:
        _ = _run(["flux", "reconcile", "source", "oci", name])

    def delete(self, name: str) -> None:
        _ = _run(["kind", "delete", "cluster", "--name", name])

    def create(self, name: str, *, registry_name: str, registry_port: int) -> None:
        self._ensure_registry(registry_name, registry_port)
        _ = _run(
            ["kind", "create", "cluster", "--name", name, "--config", "-"],
            stdin=_KIND_CONFIG,
        )
        self._configure_nodes(name, registry_name, registry_port)
        self._connect_registry_to_network(registry_name)

    def _ensure_registry(self, registry_name: str, registry_port: int) -> None:
        state = _query(["docker", "inspect", "-f", "{{.State.Running}}", registry_name])
        if state is not None and state.strip() == "true":
            return
        _ = _run(
            [
                "docker",
                "run",
                "-d",
                "--restart=always",
                "-p",
                f"127.0.0.1:{registry_port}:{_REGISTRY_CONTAINER_PORT}",
                "--network",
                "bridge",
                "--name",
                registry_name,
                "registry:2",
            ]
        )

    def _configure_nodes(
        self, name: str, registry_name: str, registry_port: int
    ) -> None:
        registry_dir = f"/etc/containerd/certs.d/localhost:{registry_port}"
        hosts_toml = f'[host."http://{registry_name}:{_REGISTRY_CONTAINER_PORT}"]\n'
        for node in _run(["kind", "get", "nodes", "--name", name]).splitlines():
            if not node:
                continue
            _ = _run(["docker", "exec", node, "mkdir", "-p", registry_dir])
            _ = _run(
                [
                    "docker",
                    "exec",
                    "-i",
                    node,
                    "cp",
                    "/dev/stdin",
                    f"{registry_dir}/hosts.toml",
                ],
                stdin=hosts_toml,
            )

    def _connect_registry_to_network(self, registry_name: str) -> None:
        connected = _query(
            [
                "docker",
                "inspect",
                "-f",
                f"{{{{json .NetworkSettings.Networks.{_KIND_NETWORK}}}}}",
                registry_name,
            ],
        )
        if connected is not None and connected.strip() != "null":
            return
        _ = _run(["docker", "network", "connect", _KIND_NETWORK, registry_name])
