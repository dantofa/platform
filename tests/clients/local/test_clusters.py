from __future__ import annotations

import subprocess
from typing import Any
from unittest.mock import patch

import pytest

from dantofa.clients.local import clusters as adapter
from dantofa.clients.local.errors import LocalClusterError


def _completed(
    stdout: str = "", returncode: int = 0, stderr: str = ""
) -> subprocess.CompletedProcess[str]:
    return subprocess.CompletedProcess(
        args=[],
        returncode=returncode,
        stdout=stdout,
        stderr=stderr,
    )


def test_list_parses_names():
    with patch("subprocess.run", return_value=_completed(stdout="a\nb\n\n")) as run:
        assert adapter.KindClient().list() == ["a", "b"]
    assert run.call_args.args[0] == ["kind", "get", "clusters"]


def test_get_kubeconfig_returns_stdout():
    with patch("subprocess.run", return_value=_completed(stdout="apiVersion: v1\n")):
        assert adapter.KindClient().get_kubeconfig("dev") == "apiVersion: v1\n"


def test_run_failure_raises():
    with (
        patch("subprocess.run", return_value=_completed(returncode=1, stderr="boom")),
        pytest.raises(LocalClusterError),
    ):
        _ = adapter.KindClient().list()


def test_missing_tool_raises():
    with (
        patch("subprocess.run", side_effect=FileNotFoundError),
        pytest.raises(LocalClusterError),
    ):
        adapter.KindClient().delete("dev")


def test_create_provisions_cluster_and_registry():
    def fake_run(args: list[str], **_: Any) -> subprocess.CompletedProcess[str]:
        if args[0] == "docker" and args[1] == "inspect":
            fmt = args[3]
            if "State.Running" in fmt:
                return _completed(stdout="true\n")  # registry already up
            if "Networks" in fmt:
                return _completed(stdout='"connected"\n')  # already on network
        if args[:3] == ["kind", "get", "nodes"]:
            return _completed(stdout="dev-control-plane\n")
        return _completed()

    with patch("subprocess.run", side_effect=fake_run) as run:
        adapter.KindClient().create("dev", registry_name="reg", registry_port=5001)
    invoked = [call.args[0] for call in run.call_args_list]
    assert ["kind", "create", "cluster", "--name", "dev", "--config", "-"] in invoked
    # hosts.toml written on the node
    assert any(a[:3] == ["docker", "exec", "dev-control-plane"] for a in invoked)
    # cluster config declares one control-plane + three workers
    create_call = next(
        c for c in run.call_args_list if c.args[0][:2] == ["kind", "create"]
    )
    config = create_call.kwargs["input"]
    assert config.count("role: worker") == 3
    assert config.count("role: control-plane") == 1


def _git_run(dirty: bool = False):
    def fake_run(args: list[str], **_: Any) -> subprocess.CompletedProcess[str]:
        if args[:3] == ["git", "config", "--get"]:
            return _completed(stdout="git@example.com:o/r.git\n")
        if args == ["git", "rev-parse", "--abbrev-ref", "HEAD"]:
            return _completed(stdout="main\n")
        if args == ["git", "rev-parse", "HEAD"]:
            return _completed(stdout="abc123\n")
        if args[:2] == ["git", "status"]:
            return _completed(stdout=" M f\n" if dirty else "")
        return _completed()

    return fake_run


def test_git_provenance_uses_remote_and_head():
    with patch("subprocess.run", side_effect=_git_run()):
        source, revision = adapter.KindClient().git_provenance()
    assert source == "git@example.com:o/r.git"
    assert revision == "main@sha1:abc123"


def test_git_provenance_marks_dirty():
    with patch("subprocess.run", side_effect=_git_run(dirty=True)):
        _, revision = adapter.KindClient().git_provenance()
    assert revision == "main@sha1:abc123-dirty"


def test_push_artifact_invokes_flux():
    with patch("subprocess.run", return_value=_completed(stdout="pushed")) as run:
        adapter.KindClient().push_artifact(
            "oci://localhost:5001/local:latest",
            path="flux/",
            source="src",
            revision="rev",
        )
    assert run.call_args.args[0] == [
        "flux",
        "push",
        "artifact",
        "oci://localhost:5001/local:latest",
        "--path",
        "flux/",
        "--source",
        "src",
        "--revision",
        "rev",
    ]


def test_reconcile_source_invokes_flux():
    with patch("subprocess.run", return_value=_completed()) as run:
        adapter.KindClient().reconcile_source("local")
    assert run.call_args.args[0] == ["flux", "reconcile", "source", "oci", "local"]
