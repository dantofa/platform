"""Adapter over boto3 (S3) for DigitalOcean Spaces buckets.

Spaces is S3-compatible and is **not** part of the DigitalOcean REST API, so
bucket lifecycle goes through boto3 against the regional Spaces endpoint
``https://<region>.digitaloceanspaces.com``. boto3 is imported lazily to keep CLI
startup cheap.
"""

from __future__ import annotations

import os
from typing import Any, cast, final

from dantofa.clients.digitalocean.errors import (
    DigitalOceanApiError,
    MissingCredentialsError,
)

KEY_ENV = "SPACES_ACCESS_KEY_ID"
SECRET_ENV = "SPACES_SECRET_ACCESS_KEY"
REGION_ENV = "DIGITALOCEAN_SPACES_REGION"
DEFAULT_REGION = "nyc3"


def _resolve_region(region: str | None) -> str:
    return region or os.environ.get(REGION_ENV) or DEFAULT_REGION


def _api_error(exc: Any) -> DigitalOceanApiError:
    """Wrap a botocore error, preserving its raw ``response`` dict as payload."""
    return DigitalOceanApiError(getattr(exc, "response", None) or str(exc))


@final
class SpacesClient:
    """Semantic wrapper over the boto3 S3 client pointed at Spaces."""

    def __init__(
        self,
        region: str | None = None,
        key_id: str | None = None,
        secret: str | None = None,
    ) -> None:
        import boto3  # noqa: PLC0415 — lazy import to keep CLI startup fast

        region = _resolve_region(region)
        key_id = key_id or os.environ.get(KEY_ENV)
        secret = secret or os.environ.get(SECRET_ENV)
        if not key_id or not secret:
            raise MissingCredentialsError(
                f"No Spaces credentials. Set ${KEY_ENV} and ${SECRET_ENV}.",
            )
        self.region: str = region
        self._client = boto3.client(
            "s3",
            region_name=region,
            endpoint_url=f"https://{region}.digitaloceanspaces.com",
            aws_access_key_id=key_id,
            aws_secret_access_key=secret,
        )

    def list(self) -> list[dict[str, Any]]:
        from botocore.exceptions import ClientError  # noqa: PLC0415

        try:
            buckets = self._client.list_buckets().get("Buckets", [])
            return cast("list[dict[str, Any]]", buckets)
        except ClientError as exc:
            raise _api_error(exc) from exc

    def create(self, name: str) -> None:
        """Create a bucket. Surfaces the raw S3 error (incl. already-exists)."""
        from botocore.exceptions import ClientError  # noqa: PLC0415

        try:
            _ = self._client.create_bucket(Bucket=name)
        except ClientError as exc:
            raise _api_error(exc) from exc

    def delete(self, name: str) -> None:
        from botocore.exceptions import ClientError  # noqa: PLC0415

        try:
            _ = self._client.delete_bucket(Bucket=name)
        except ClientError as exc:
            raise _api_error(exc) from exc
