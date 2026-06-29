"""Application logic for DigitalOcean Spaces bucket management.

Framework-free. The Spaces (S3) surface is reached through a structurally-typed
:class:`SupportsSpacesApi` client; the concrete boto3 adapter lives in
``dantofa.clients.digitalocean.spaces``.
"""

from __future__ import annotations

from typing import Protocol


class SupportsSpacesApi(Protocol):
    """The Spaces-API surface this module depends on (see clients adapter)."""

    def list(self) -> list[dict[str, object]]: ...
    def create(self, name: str) -> None: ...
    def delete(self, name: str) -> None: ...


def list_buckets(client: SupportsSpacesApi) -> list[dict[str, object]]:
    return client.list()


def create_bucket(client: SupportsSpacesApi, name: str) -> None:
    """Create a bucket. Surfaces the raw S3 error if it already exists."""
    client.create(name)


def delete_bucket(client: SupportsSpacesApi, name: str) -> None:
    # SKY-D216 is a false positive here: delete() is a bucket-name Protocol call,
    # not a URL/HTTP sink — there is no SSRF surface in this module.
    client.delete(name)  # skylos: ignore[SKY-D216]
