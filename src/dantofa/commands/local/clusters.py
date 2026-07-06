"""Typer commands for local (kind) development clusters.

Thin presentation layer over ``dantofa.core.local.clusters`` and the kind adapter.
"""

from __future__ import annotations

from pathlib import Path
from typing import Annotated

import typer

from dantofa.clients.local.clusters import KindClient
from dantofa.clients.local.errors import LocalClusterError
from dantofa.commands.utils import echo_error, echo_json, write_owner_only
from dantofa.core.local import clusters as core

cluster_app = typer.Typer(
    help="Manage local (kind) development clusters.",
    no_args_is_help=True,
)

_EXPECTED_ERRORS = (
    LocalClusterError,
    core.LocalClusterExistsError,
    core.LocalClusterNotFoundError,
)

_Name = Annotated[str, typer.Argument(help="Local cluster name.")]


@cluster_app.command("list")
def list_() -> None:
    """List local clusters."""
    try:
        clusters = core.list_clusters(KindClient())
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc
    echo_json(clusters)


@cluster_app.command()
def create(
    name: _Name = core.DEFAULT_CLUSTER_NAME,
    registry_name: Annotated[
        str,
        typer.Option(
            "--registry-name", help="Name of the internal OCI registry container."
        ),
    ] = core.DEFAULT_REGISTRY_NAME,
    registry_port: Annotated[
        int,
        typer.Option("--registry-port", help="Host port the registry is pushable on."),
    ] = core.DEFAULT_REGISTRY_PORT,
) -> None:
    """Create a kind cluster wired to an internal OCI registry.

    The registry is pushable from ``localhost:<registry-port>`` and reachable
    in-cluster as ``<registry-name>:5000`` (for Flux OCIRepository sources).
    """
    try:
        result = core.create_cluster(
            KindClient(),
            name,
            registry_name=registry_name,
            registry_port=registry_port,
        )
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc
    echo_json(result)


@cluster_app.command()
def push(
    path: Annotated[
        str,
        typer.Option("--path", "-p", help="Directory to package as the OCI artifact."),
    ] = core.DEFAULT_ARTIFACT_PATH,
    name: Annotated[
        str,
        typer.Option("--name", help="OCI repository name (matches the OCIRepository)."),
    ] = core.DEFAULT_ARTIFACT_NAME,
    tag: Annotated[
        str,
        typer.Option("--tag", "-t", help="OCI tag."),
    ] = core.DEFAULT_ARTIFACT_TAG,
    registry_port: Annotated[
        int,
        typer.Option("--registry-port", help="Host port of the local registry."),
    ] = core.DEFAULT_REGISTRY_PORT,
) -> None:
    """Publish the project as an OCI artifact and reconcile Flux.

    Pushes ``<path>`` to ``localhost:<registry-port>/<name>:<tag>`` (stamped with
    the working tree's git provenance) and always reconciles the Flux
    OCIRepository named ``<name>`` so the cluster picks it up immediately.
    """
    try:
        result = core.push_artifact(
            KindClient(),
            name=name,
            tag=tag,
            path=path,
            registry_port=registry_port,
        )
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc
    echo_json(result)


@cluster_app.command()
def delete(name: _Name = core.DEFAULT_CLUSTER_NAME) -> None:
    """Delete a local cluster. Idempotent: succeeds if it is already absent."""
    try:
        _ = core.delete_cluster(KindClient(), name)
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc


@cluster_app.command()
def connect(
    name: _Name = core.DEFAULT_CLUSTER_NAME,
    output: Annotated[
        Path,
        typer.Option("--output", "-o", help="Where to write the kubeconfig."),
    ] = Path(".kubeconfig"),
) -> None:
    """Write a local cluster's kubeconfig to a file."""
    try:
        kubeconfig = core.get_kubeconfig(KindClient(), name)
    except _EXPECTED_ERRORS as exc:
        echo_error(exc)
        raise typer.Exit(1) from exc
    write_owner_only(output, kubeconfig)
    echo_json({"name": name, "kubeconfig": str(output)})
