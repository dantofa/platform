"""The ``local`` command group: local development clusters (kind).

Wired into the root app in ``dantofa.cli.main``.
"""

from __future__ import annotations

import typer

from dantofa.commands.local.clusters import cluster_app

local_app = typer.Typer(
    help="Manage local development clusters.",
    no_args_is_help=True,
)
local_app.add_typer(cluster_app, name="cluster")
