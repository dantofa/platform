"""Application logic for local (kind) development clusters.

Framework-free: no typer, no subprocess. The kind/docker/kubectl surface is
reached through a structurally-typed :class:`SupportsLocalClusterApi` client (the
concrete adapter lives in ``dantofa.clients.local.clusters``), keeping this module
unit-testable with a fake client.

Local clusters are provisioned with an internal OCI registry that is pushable
from ``localhost`` and reachable in-cluster (see the adapter), so Flux
``OCIRepository`` sources can pull artifacts pushed straight from a workstation.
"""

from __future__ import annotations

from typing import Protocol

DEFAULT_CLUSTER_NAME = "local"
DEFAULT_REGISTRY_NAME = "kind-registry"
DEFAULT_REGISTRY_PORT = 5001
DEFAULT_ARTIFACT_NAME = "local"
DEFAULT_ARTIFACT_TAG = "latest"
DEFAULT_ARTIFACT_PATH = "flux/"


class SupportsLocalClusterApi(Protocol):
    """The local-cluster surface this module depends on (see clients adapter)."""

    def list(self) -> list[str]: ...
    def create(self, name: str, *, registry_name: str, registry_port: int) -> None: ...
    def delete(self, name: str) -> None: ...
    def get_kubeconfig(self, name: str) -> str: ...
    def git_provenance(self) -> tuple[str, str]: ...
    def push_artifact(
        self, url: str, *, path: str, source: str, revision: str
    ) -> None: ...
    def reconcile_source(self, name: str) -> None: ...


class LocalClusterExistsError(RuntimeError):
    """A local cluster with the requested name already exists."""

    def __init__(self, name: str) -> None:
        super().__init__(f"A local cluster named {name!r} already exists.")


class LocalClusterNotFoundError(LookupError):
    """No local cluster matched the given name."""

    def __init__(self, name: str) -> None:
        super().__init__(f"No local cluster named {name!r}.")


def list_clusters(client: SupportsLocalClusterApi) -> list[str]:
    return client.list()


def create_cluster(
    client: SupportsLocalClusterApi,
    name: str,
    *,
    registry_name: str = DEFAULT_REGISTRY_NAME,
    registry_port: int = DEFAULT_REGISTRY_PORT,
) -> dict[str, object]:
    """Create a kind cluster wired to an internal OCI registry.

    Returns the push endpoints: ``registry`` (from localhost) and
    ``registry_in_cluster`` (the address in-cluster workloads/Flux use).
    """
    if name in client.list():
        raise LocalClusterExistsError(name)
    client.create(name, registry_name=registry_name, registry_port=registry_port)
    return {
        "name": name,
        "registry": f"localhost:{registry_port}",
        "registry_in_cluster": f"{registry_name}:5000",
    }


def delete_cluster(client: SupportsLocalClusterApi, name: str) -> str | None:
    """Delete a local cluster. Idempotent: a missing cluster is a no-op."""
    if name not in client.list():
        return None
    # SKY-D216 false positive: delete() is a name Protocol call, not a URL sink.
    client.delete(name)  # skylos: ignore[SKY-D216]
    return name


def push_artifact(
    client: SupportsLocalClusterApi,
    *,
    name: str = DEFAULT_ARTIFACT_NAME,
    tag: str = DEFAULT_ARTIFACT_TAG,
    path: str = DEFAULT_ARTIFACT_PATH,
    registry_port: int = DEFAULT_REGISTRY_PORT,
) -> dict[str, object]:
    """Publish ``path`` as an OCI artifact to the local registry and reconcile.

    Pushes to ``localhost:<registry_port>/<name>:<tag>`` (stamped with the git
    provenance of the working tree) and always reconciles the Flux OCIRepository
    named ``<name>`` so the cluster picks it up immediately.
    """
    reference = f"localhost:{registry_port}/{name}:{tag}"
    source, revision = client.git_provenance()
    client.push_artifact(
        f"oci://{reference}", path=path, source=source, revision=revision
    )
    client.reconcile_source(name)
    return {
        "artifact": reference,
        "path": path,
        "source": source,
        "revision": revision,
        "reconciled": name,
    }


def get_kubeconfig(client: SupportsLocalClusterApi, name: str) -> str:
    """Return the kubeconfig for the named local cluster."""
    if name not in client.list():
        raise LocalClusterNotFoundError(name)
    # SKY-D216 false positive: get_kubeconfig() is a name→config Protocol call,
    # not an HTTP/URL sink — the adapter shells out to kind.
    return client.get_kubeconfig(name)  # skylos: ignore[SKY-D216]
