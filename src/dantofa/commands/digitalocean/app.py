"""The ``do`` command group: DigitalOcean resources.

Aggregates the per-resource Typer sub-apps. Wired into the root app in
``dantofa.cli.main``.
"""

from __future__ import annotations

import typer

from dantofa.commands.digitalocean.clusters import cluster_app
from dantofa.commands.digitalocean.spaces import spaces_app

do_app = typer.Typer(
    help="Manage DigitalOcean resources.",
    no_args_is_help=True,
)
do_app.add_typer(cluster_app, name="cluster")
do_app.add_typer(spaces_app, name="spaces")
