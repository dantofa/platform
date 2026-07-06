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

import time
from collections.abc import Callable, Mapping, Sequence
from typing import Protocol, cast

_RUNNING_STATE = "running"
# States from which a freshly-created cluster will never reach "running".
_FAILED_STATES = frozenset({"error", "deleted", "deleting"})
_DEFAULT_WAIT_TIMEOUT = 900.0  # seconds (~15 min)
_DEFAULT_POLL_INTERVAL = 10.0  # seconds


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
    def get(self, cluster_id: str) -> dict[str, object]: ...
    def get_kubeconfig(self, cluster_id: str) -> str: ...


class ClusterNotFoundError(LookupError):
    """No cluster matched the given name or id."""

    def __init__(self, identifier: str) -> None:
        super().__init__(f"No cluster found matching {identifier!r}.")


class ClusterNotReadyError(RuntimeError):
    """A cluster did not reach the running state (terminal failure or timeout)."""

    def __init__(self, name: str, state: str, *, timed_out: bool = False) -> None:
        reason = "timed out waiting" if timed_out else f"entered state {state!r}"
        super().__init__(f"Cluster {name!r} did not become ready: {reason}.")


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


def _state(cluster: Mapping[str, object]) -> str | None:
    status = cast("Mapping[str, object]", cluster.get("status") or {})
    state = status.get("state")
    return state if isinstance(state, str) else None


def wait_for_running(
    client: SupportsClusterApi,
    name: str,
    *,
    timeout: float = _DEFAULT_WAIT_TIMEOUT,
    interval: float = _DEFAULT_POLL_INTERVAL,
    sleep: Callable[[float], object] = time.sleep,
    monotonic: Callable[[], float] = time.monotonic,
) -> dict[str, object]:
    """Poll until the named cluster reaches the running state; return it.

    Raises :class:`ClusterNotReadyError` on a terminal failure state or when
    ``timeout`` seconds elapse. ``sleep``/``monotonic`` are injectable for tests.
    """
    existing = _resolve(client.list(), name)
    if existing is None:
        raise ClusterNotFoundError(name)
    cluster_id = str(existing["id"])
    deadline = monotonic() + timeout
    while True:
        # SKY-D216 false positive: get() is a cluster-id Protocol call, not an
        # HTTP/URL sink — the adapter owns the request.
        cluster = client.get(cluster_id)  # skylos: ignore[SKY-D216]
        state = _state(cluster)
        if state == _RUNNING_STATE:
            return cluster
        if state in _FAILED_STATES:
            raise ClusterNotReadyError(name, state or "unknown")
        if monotonic() >= deadline:
            raise ClusterNotReadyError(name, state or "unknown", timed_out=True)
        _ = sleep(interval)


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
