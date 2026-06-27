"""Shared helpers for the typer command modules.

Every CLI command follows the same shape — load the kubeconfig if needed, run an
async body, print its result as JSON, and turn the expected operational errors into
a clean stderr message and a non-zero exit. ``run_async`` and ``serialize`` capture
the common parts; each command module keeps its own ``except`` clause (the expected
error types differ per module) and renders via ``echo_error`` or a module-specific
variant.
"""

from __future__ import annotations

import dataclasses
import json
from typing import cast

import typer


def serialize(value: object) -> object:
    """Recursively turn dataclasses (and lists of them) into JSON-ready structures."""
    if dataclasses.is_dataclass(value) and not isinstance(value, type):
        return dataclasses.asdict(value)
    if isinstance(value, list):
        return [serialize(item) for item in cast("list[object]", value)]
    return value


def echo_error(exc: Exception) -> None:
    """Render an exception as a ``{"code", "message"}`` JSON object on stderr."""
    typer.echo(
        json.dumps({"code": type(exc).__name__, "message": str(exc)}, indent=2),
        err=True,
    )
