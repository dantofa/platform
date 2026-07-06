from __future__ import annotations

from typing import final

import pytest

from dantofa.core.local import clusters as core


@final
class FakeLocalApi:
    def __init__(self, existing: list[str] | None = None) -> None:
        self.existing: list[str] = existing or []
        self.created: tuple[str, str, int] | None = None
        self.deleted: str | None = None
        self.pushed: tuple[str, str, str, str] | None = None
        self.reconciled: str | None = None

    def list(self) -> list[str]:
        return list(self.existing)

    def create(self, name: str, *, registry_name: str, registry_port: int) -> None:
        self.created = (name, registry_name, registry_port)
        self.existing.append(name)

    def delete(self, name: str) -> None:
        self.deleted = name

    def get_kubeconfig(self, name: str) -> str:
        return f"kubeconfig-{name}"

    def git_provenance(self) -> tuple[str, str]:
        return ("git@example.com:o/r.git", "main@sha1:abc123")

    def push_artifact(self, url: str, *, path: str, source: str, revision: str) -> None:
        self.pushed = (url, path, source, revision)

    def reconcile_source(self, name: str) -> None:
        self.reconciled = name


def test_create_returns_registry_endpoints():
    client = FakeLocalApi()
    result = core.create_cluster(client, "dev", registry_name="reg", registry_port=5001)
    assert client.created == ("dev", "reg", 5001)
    assert result == {
        "name": "dev",
        "registry": "localhost:5001",
        "registry_in_cluster": "reg:5000",
    }


def test_create_existing_raises():
    client = FakeLocalApi(["dev"])
    with pytest.raises(core.LocalClusterExistsError):
        _ = core.create_cluster(client, "dev")


def test_delete_absent_is_noop():
    client = FakeLocalApi()
    assert core.delete_cluster(client, "gone") is None
    assert client.deleted is None


def test_delete_present():
    client = FakeLocalApi(["dev"])
    assert core.delete_cluster(client, "dev") == "dev"
    assert client.deleted == "dev"


def test_get_kubeconfig_missing_raises():
    client = FakeLocalApi()
    with pytest.raises(core.LocalClusterNotFoundError):
        _ = core.get_kubeconfig(client, "dev")


def test_get_kubeconfig_present():
    client = FakeLocalApi(["dev"])
    assert core.get_kubeconfig(client, "dev") == "kubeconfig-dev"


def test_push_artifact_builds_reference_and_reconciles():
    client = FakeLocalApi()
    result = core.push_artifact(
        client, name="local", tag="latest", path="flux/", registry_port=5001
    )
    assert client.pushed == (
        "oci://localhost:5001/local:latest",
        "flux/",
        "git@example.com:o/r.git",
        "main@sha1:abc123",
    )
    assert client.reconciled == "local"  # reconcile always runs
    assert result["artifact"] == "localhost:5001/local:latest"
    assert result["revision"] == "main@sha1:abc123"
    assert result["reconciled"] == "local"


def test_list_passthrough():
    assert core.list_clusters(FakeLocalApi(["a", "b"])) == ["a", "b"]
