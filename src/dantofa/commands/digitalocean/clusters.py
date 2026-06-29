"""Typer commands for DigitalOcean Kubernetes (DOKS) clusters.

Thin presentation layer: parse options, build the client adapter, delegate to
``dantofa.core.digitalocean.clusters``, render JSON. No logic lives here.
"""

from __future__ import annotations

from typing import Annotated

import typer

from dantofa.clients.digitalocean.clusters import ClusterClient
from dantofa.clients.digitalocean.errors import (
    DigitalOceanApiError,
    MissingCredentialsError,
)
from dantofa.commands.utils import echo_error, echo_json
from dantofa.core.digitalocean import clusters as core

cluster_app = typer.Typer(
    help="Manage DigitalOcean Kubernetes (DOKS) clusters.",
    no_args_is_help=True,
)

# Operational errors that map to a clean stderr render + non-zero exit. DO API
# failures carry the provider's raw error body (see echo_error).
_EXPECTED_ERRORS = (
    MissingCredentialsError,
    DigitalOceanApiError,
    core.ClusterNotFoundError,
)

Token = Annotated[
    str | None,
    typer.Option(
        help="DigitalOcean API token (defaults to $DIGITALOCEAN_ACCESS_TOKEN)."
    ),
]


@cluster_app.command("list")
def list_(token: Token = None) -> None:
    """List all clusters."""
    try:
        clusters = core.list_clusters(ClusterClient(token=token))
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc
    echo_json(clusters)


@cluster_app.command()
def create(
    name: Annotated[str, typer.Option(help="Cluster name.")],
    region: Annotated[str, typer.Option(help="Region slug, e.g. nyc3.")] = "nyc3",
    version: Annotated[
        str, typer.Option(help='Kubernetes version slug, or "latest".')
    ] = "latest",
    node_pool_size: Annotated[
        str,
        typer.Option("--node-pool-size", help="Primary node pool droplet size slug."),
    ] = "s-2vcpu-4gb",
    node_pool_count: Annotated[
        int, typer.Option("--node-pool-count", help="Initial node count.")
    ] = 2,
    node_pool_min: Annotated[
        int, typer.Option("--node-pool-min", help="Minimum nodes (autoscaling).")
    ] = 2,
    node_pool_max: Annotated[
        int, typer.Option("--node-pool-max", help="Maximum nodes (autoscaling).")
    ] = 10,
    ha: Annotated[
        bool,
        typer.Option("--ha", help="Enable HA control plane"),
    ] = False,
    tag: Annotated[
        list[str],
        typer.Option("--tag", help="A cluster tag; repeatable."),
    ] = [],
    token: Token = None,
) -> None:
    """Create a DOKS cluster.

    The node pool is always named "system" with autoscaling enabled, and
    auto-upgrade and surge-upgrade are always on. Only HA, node sizing and tags
    are configurable.
    """
    try:
        pool = core.build_node_pool(
            name="system",
            size=node_pool_size,
            count=node_pool_count,
            min_nodes=node_pool_min,
            max_nodes=node_pool_max,
        )
        body = core.build_create_body(
            name=name,
            region=region,
            version=version,
            node_pool=pool,
            tags=list(tag),
            ha=ha,
        )
        result = core.create_cluster(ClusterClient(token=token), body)
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc
    echo_json(result)


@cluster_app.command()
def update(
    name: Annotated[str, typer.Argument(help="Cluster name to update.")],
    ha: Annotated[
        bool,
        typer.Option("--ha", help="Enable HA control plane"),
    ] = False,
    tag: Annotated[
        list[str],
        typer.Option("--tag", help="Replace the cluster tags; repeatable."),
    ] = [],
    clear_tags: Annotated[
        bool,
        typer.Option("--clear-tags", help="Remove all tags from the cluster."),
    ] = False,
    token: Token = None,
) -> None:
    """Update a cluster's mutable fields (HA, tags).

    Auto-upgrade and surge-upgrade are re-asserted as enabled on every update.
    """
    if clear_tags and tag:
        raise typer.BadParameter("--clear-tags and --tag are mutually exclusive.")
    try:
        # No tag flags means "leave tags untouched"; --clear-tags sends [] to
        # wipe them; --tag replaces them. Only --tag/--clear-tags touch tags.
        tags = [] if clear_tags else (list(tag) if tag else None)
        body = core.build_update_body(tags=tags, ha=ha)
        result = core.update_cluster(ClusterClient(token=token), name, body)
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc
    echo_json(result)


@cluster_app.command()
def delete(
    name: Annotated[str, typer.Argument(help="Cluster name.")],
    token: Token = None,
) -> None:
    """Delete a cluster by name. Idempotent: succeeds if it is already absent."""
    try:
        _ = core.delete_cluster(ClusterClient(token=token), name)
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc
