from __future__ import annotations

from collections.abc import Mapping
from typing import final

import pytest

from dantofa.core.digitalocean import clusters as core


@final
class FakeClusterApi:
    """In-memory stand-in for the cluster client adapter."""

    def __init__(self, clusters: list[dict[str, object]] | None = None) -> None:
        self.clusters: list[dict[str, object]] = clusters or []
        self.created: dict[str, object] | None = None
        self.updated: tuple[str, dict[str, object]] | None = None
        self.deleted: str | None = None
        self.kubeconfig_for: str | None = None

    def list(self) -> list[dict[str, object]]:
        return list(self.clusters)

    def get_kubeconfig(self, cluster_id: str) -> str:
        self.kubeconfig_for = cluster_id
        return f"kubeconfig-for-{cluster_id}"

    def create(self, body: Mapping[str, object]) -> dict[str, object]:
        self.created = dict(body)
        return {"id": "new-id", **dict(body)}

    def update(self, cluster_id: str, body: Mapping[str, object]) -> dict[str, object]:
        self.updated = (cluster_id, dict(body))
        return {"id": cluster_id, **dict(body)}

    def delete(self, cluster_id: str) -> None:
        self.deleted = cluster_id


def test_build_create_body_forces_invariants():
    body = core.build_create_body(
        name="c1",
        region="nyc3",
        version="latest",
        node_pool={"name": "system", "size": "s", "count": 2},
    )
    assert body == {
        "name": "c1",
        "region": "nyc3",
        "version": "latest",
        "node_pools": [{"name": "system", "size": "s", "count": 2}],
        "auto_upgrade": True,
        "surge_upgrade": True,
        "tags": [],
    }
    assert "ha" not in body  # ha omitted unless enabled


def test_build_create_body_with_tags_and_ha():
    body = core.build_create_body(
        name="c1",
        region="nyc3",
        version="latest",
        node_pool={"name": "system"},
        tags=["team"],
        ha=True,
    )
    assert body["tags"] == ["team"]
    assert body["ha"] is True
    assert body["node_pools"] == [{"name": "system"}]
    assert body["auto_upgrade"] is True
    assert body["surge_upgrade"] is True


def test_build_update_body_reasserts_invariants():
    # auto/surge upgrade are always re-asserted; tags/ha only when supplied.
    assert core.build_update_body() == {"auto_upgrade": True, "surge_upgrade": True}
    assert core.build_update_body(tags=["a"], ha=True) == {
        "auto_upgrade": True,
        "surge_upgrade": True,
        "tags": ["a"],
        "ha": True,
    }
    # ha is enable-only: ha=False is never emitted.
    assert "ha" not in core.build_update_body(ha=False)
    # an explicit empty list clears tags (distinct from None = untouched).
    assert core.build_update_body(tags=[]) == {
        "auto_upgrade": True,
        "surge_upgrade": True,
        "tags": [],
    }


def test_build_node_pool_always_autoscales():
    pool = core.build_node_pool(
        name="system",
        size="s-2vcpu-4gb",
        count=2,
        min_nodes=2,
        max_nodes=10,
    )
    assert pool == {
        "name": "system",
        "size": "s-2vcpu-4gb",
        "count": 2,
        "auto_scale": True,
        "min_nodes": 2,
        "max_nodes": 10,
    }


def test_create_cluster_delegates():
    client = FakeClusterApi([])
    result = core.create_cluster(client, {"name": "c1"})
    assert client.created == {"name": "c1"}
    assert result["name"] == "c1"


def test_update_cluster_by_name_resolves_id():
    client = FakeClusterApi([{"id": "abc", "name": "c1"}])
    result = core.update_cluster(client, "c1", {"auto_upgrade": True})
    assert client.updated is not None
    cluster_id, body = client.updated
    assert cluster_id == "abc"  # name resolved to id for the API call
    assert body == {"name": "c1", "auto_upgrade": True}
    assert result["id"] == "abc"


def test_update_cluster_unknown_name_raises():
    # identification is by name only: an id is not a valid identifier here.
    client = FakeClusterApi([{"id": "abc", "name": "c1"}])
    with pytest.raises(core.ClusterNotFoundError):
        _ = core.update_cluster(client, "abc", {"ha": True})


def test_delete_by_name_resolves_id():
    client = FakeClusterApi([{"id": "abc", "name": "c1"}])
    assert core.delete_cluster(client, "c1") == "abc"
    assert client.deleted == "abc"


def test_delete_absent_is_noop():
    # idempotent: deleting a name that doesn't exist returns None, no error.
    client = FakeClusterApi([{"id": "abc", "name": "c1"}])
    assert core.delete_cluster(client, "does-not-exist") is None
    assert client.deleted is None


def test_get_kubeconfig_resolves_id():
    client = FakeClusterApi([{"id": "abc", "name": "c1"}])
    assert core.get_kubeconfig(client, "c1") == "kubeconfig-for-abc"
    assert client.kubeconfig_for == "abc"


def test_get_kubeconfig_missing_raises():
    client = FakeClusterApi([])
    with pytest.raises(core.ClusterNotFoundError):
        _ = core.get_kubeconfig(client, "nope")


def test_list_passthrough():
    client = FakeClusterApi([{"id": "1"}])
    assert core.list_clusters(client) == [{"id": "1"}]
