"""Application logic for DigitalOcean Kubernetes (DOKS) cluster management.

Framework-free: no typer, no SDK imports. The cluster API is reached through a
structurally-typed :class:`SupportsClusterApi` client (the concrete adapter lives
in ``dantofa.clients.digitalocean.clusters``), which keeps this module trivially
unit-testable with a fake/mock client.

These functions mirror the DigitalOcean API one-to-one (create, update, delete,
list) and do no validation of their own — DigitalOcean is the authority on valid
regions/versions/sizes, and its error responses are surfaced to the user as-is.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from typing import Protocol


class SupportsClusterApi(Protocol):
    """The cluster-API surface this module depends on (see clients adapter)."""

    def list(self) -> list[dict[str, object]]: ...
    def create(self, body: Mapping[str, object]) -> dict[str, object]: ...
    def update(
        self,
        cluster_id: str,
        body: Mapping[str, object],
    ) -> dict[str, object]: ...
    def delete(self, cluster_id: str) -> None: ...
    def get_kubeconfig(self, cluster_id: str) -> str: ...


class ClusterNotFoundError(LookupError):
    """No cluster matched the given name or id."""

    def __init__(self, identifier: str) -> None:
        super().__init__(f"No cluster found matching {identifier!r}.")


def build_node_pool(
    *,
    name: str,
    size: str,
    count: int,
    min_nodes: int,
    max_nodes: int,
) -> dict[str, object]:
    """Assemble a node pool (opinionated: autoscaling is always on)."""
    return {
        "name": name,
        "size": size,
        "count": count,
        "auto_scale": True,
        "min_nodes": min_nodes,
        "max_nodes": max_nodes,
    }


def build_create_body(
    *,
    name: str,
    region: str,
    version: str,
    node_pool: dict[str, object],
    tags: Sequence[str] = (),
    ha: bool = False,
) -> dict[str, object]:
    """Assemble the create body (POST /v2/kubernetes/clusters).

    Takes the single node pool the CLI ever creates and wraps it in the array the
    API expects. Opinionated: auto-upgrade and surge-upgrade are always enabled.
    """
    body: dict[str, object] = {
        "name": name,
        "region": region,
        "version": version,
        "node_pools": [node_pool],
        "auto_upgrade": True,
        "surge_upgrade": True,
        "tags": list(tags),
    }
    if ha:
        body["ha"] = ha
    return body


def build_update_body(
    *,
    tags: list[str] | None = None,
    ha: bool = False,
) -> dict[str, object]:
    """Assemble the update body (PUT /v2/kubernetes/clusters/{id}).

    Opinionated: auto-upgrade and surge-upgrade are always re-asserted as enabled.
    ``tags`` and ``ha`` are included only when supplied.
    """
    body: dict[str, object] = {
        "auto_upgrade": True,
        "surge_upgrade": True,
    }
    if tags is not None:
        body["tags"] = tags
    if ha:
        body["ha"] = ha
    return body


def list_clusters(client: SupportsClusterApi) -> list[dict[str, object]]:
    return client.list()


def create_cluster(
    client: SupportsClusterApi,
    body: Mapping[str, object],
) -> dict[str, object]:
    """Create a cluster. Returns the cluster DigitalOcean reports back."""
    return client.create(body)


def _resolve(
    clusters: list[dict[str, object]],
    name: str,
) -> dict[str, object] | None:
    """Find a cluster by name. Clusters are identified by name only."""
    return next((c for c in clusters if c.get("name") == name), None)


def update_cluster(
    client: SupportsClusterApi,
    name: str,
    body: Mapping[str, object],
) -> dict[str, object]:
    """Update the named cluster with the given mutable fields.

    Clusters are identified by name (the id is resolved internally for the API
    call). The DO update endpoint requires ``name``, so it is always re-sent;
    there is no rename option.
    """
    existing = _resolve(client.list(), name)
    if existing is None:
        raise ClusterNotFoundError(name)
    payload: dict[str, object] = {"name": name, **body}
    return client.update(str(existing["id"]), payload)


def get_kubeconfig(client: SupportsClusterApi, name: str) -> str:
    """Return the kubeconfig for the named cluster."""
    existing = _resolve(client.list(), name)
    if existing is None:
        raise ClusterNotFoundError(name)
    return client.get_kubeconfig(str(existing["id"]))


def delete_cluster(client: SupportsClusterApi, name: str) -> str | None:
    """Delete the named cluster. Idempotent: a missing cluster is a no-op.

    Returns the resolved cluster id when one was deleted, or ``None`` if no
    cluster by that name exists.
    """
    match = _resolve(client.list(), name)
    if match is None:
        return None
    cluster_id = str(match["id"])
    client.delete(cluster_id)  # skylos: ignore[SKY-D216]
    return cluster_id
