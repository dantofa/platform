"""Adapter over the pydo SDK for DigitalOcean Kubernetes (DOKS) clusters.

This lives in the clients layer because pydo is an untyped third-party SDK, so
values crossing this boundary are ``Any`` (see the basedpyright execution
environment for ``src/dantofa/clients`` in pyproject). The SDK is imported lazily
so importing the CLI does not pull in pydo's heavy azure-core transport until a
cluster command actually runs.
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from typing import Any, final

from dantofa.clients.digitalocean.errors import (
    DigitalOceanApiError,
    MissingCredentialsError,
)

TOKEN_ENV = "DIGITALOCEAN_ACCESS_TOKEN"

# DigitalOcean caps per_page at 200; request the max to minimise round-trips.
_PER_PAGE = 200


def _resolve_token(token: str | None) -> str:
    resolved = token or os.environ.get(TOKEN_ENV)
    if not resolved:
        raise MissingCredentialsError(
            f"No DigitalOcean API token. Pass --token or set ${TOKEN_ENV}.",
        )
    return resolved


def _api_error(exc: Any) -> DigitalOceanApiError:
    """Wrap an azure-core error, preserving DigitalOcean's raw JSON error body."""
    response = getattr(exc, "response", None)
    if response is not None:
        try:
            return DigitalOceanApiError(response.json())
        except Exception:  # noqa: BLE001 - body absent/non-JSON; fall back to text
            return DigitalOceanApiError(str(exc))
    return DigitalOceanApiError(str(exc))


@final
class ClusterClient:
    """Semantic wrapper over pydo's ``kubernetes`` operations.

    Returns plain dicts/lists; raises :class:`DigitalOceanApiError` on API
    failures so callers stay free of pydo/azure-core error types.
    """

    def __init__(self, token: str | None = None) -> None:
        from pydo import Client  # noqa: PLC0415 — lazy: avoid eager azure-core import

        self._client = Client(token=_resolve_token(token))

    def list(self) -> list[dict[str, Any]]:
        """Return every cluster, following pagination links."""
        from azure.core.exceptions import HttpResponseError  # noqa: PLC0415

        clusters: list[dict[str, Any]] = []
        page = 1
        try:
            while True:
                resp = self._client.kubernetes.list_clusters(
                    per_page=_PER_PAGE,
                    page=page,
                )
                batch = list(resp.get("kubernetes_clusters") or [])
                clusters.extend(batch)
                links: dict[str, Any] = resp.get("links") or {}
                pages: dict[str, Any] = links.get("pages") or {}
                if not batch or not pages.get("next"):
                    break
                page += 1
        except HttpResponseError as exc:
            raise _api_error(exc) from exc
        return clusters

    def create(self, body: Mapping[str, Any]) -> dict[str, Any]:
        from azure.core.exceptions import HttpResponseError  # noqa: PLC0415

        try:
            return dict(self._client.kubernetes.create_cluster(body=dict(body)))
        except HttpResponseError as exc:
            raise _api_error(exc) from exc

    def update(self, cluster_id: str, body: Mapping[str, Any]) -> dict[str, Any]:
        from azure.core.exceptions import HttpResponseError  # noqa: PLC0415

        try:
            return dict(
                self._client.kubernetes.update_cluster(
                    cluster_id=cluster_id,
                    body=dict(body),
                ),
            )
        except HttpResponseError as exc:
            raise _api_error(exc) from exc

    def delete(self, cluster_id: str) -> None:
        from azure.core.exceptions import HttpResponseError  # noqa: PLC0415

        try:
            _ = self._client.kubernetes.delete_cluster(cluster_id=cluster_id)
        except HttpResponseError as exc:
            raise _api_error(exc) from exc
