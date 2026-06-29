"""Typer commands for DigitalOcean Spaces buckets.

Thin presentation layer over ``dantofa.core.digitalocean.spaces`` and the boto3
adapter. Spaces buckets are S3-compatible, not part of the DO REST API.
"""

from __future__ import annotations

from typing import Annotated

import typer

from dantofa.clients.digitalocean.errors import (
    DigitalOceanApiError,
    MissingCredentialsError,
)
from dantofa.clients.digitalocean.spaces import SpacesClient
from dantofa.commands.utils import echo_error, echo_json
from dantofa.core.digitalocean import spaces as core

spaces_app = typer.Typer(
    help="Manage DigitalOcean Spaces buckets.",
    no_args_is_help=True,
)

_EXPECTED_ERRORS = (MissingCredentialsError, DigitalOceanApiError)

_RegionOption = Annotated[
    str | None,
    typer.Option(
        help="Spaces region slug, e.g. nyc3 (defaults to $DIGITALOCEAN_SPACES_REGION)."
    ),
]


@spaces_app.command("list")
def list_(region: _RegionOption = None) -> None:
    """List all Spaces buckets."""
    try:
        buckets = core.list_buckets(SpacesClient(region=region))
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc
    echo_json(buckets)


@spaces_app.command()
def create(
    name: Annotated[str, typer.Argument(help="Bucket name.")],
    region: _RegionOption = None,
) -> None:
    """Create a bucket."""
    try:
        core.create_bucket(SpacesClient(region=region), name)
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc
    echo_json({"created": name})


@spaces_app.command()
def delete(
    name: Annotated[str, typer.Argument(help="Bucket name.")],
    region: _RegionOption = None,
) -> None:
    """Delete a bucket (must be empty)."""
    try:
        core.delete_bucket(SpacesClient(region=region), name)
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc
    echo_json({"deleted": name})
