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
import os
from pathlib import Path
from typing import cast

import typer

from dantofa.clients.digitalocean.errors import DigitalOceanApiError


def write_owner_only(path: Path, content: str) -> None:
    """Write text to a file created 0600 and refusing to follow a symlink target.

    For credential-bearing outputs (kubeconfigs). SKY-D215 (tainted path) is a
    false positive: ``path`` is an operator-chosen CLI output, not an
    attacker-controlled value.
    """
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW
    fd = os.open(path, flags, 0o600)  # skylos: ignore[SKY-D215]
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        _ = handle.write(content)


def serialize(value: object) -> object:
    """Recursively turn dataclasses (and lists of them) into JSON-ready structures."""
    if dataclasses.is_dataclass(value) and not isinstance(value, type):
        return dataclasses.asdict(value)
    if isinstance(value, list):
        return [serialize(item) for item in cast("list[object]", value)]
    return value


def echo_json(value: object) -> None:
    """Render a value as pretty JSON on stdout.

    ``default=str`` covers non-JSON-native values that SDKs return (e.g. the
    ``datetime`` in boto3's bucket listings).
    """
    typer.echo(json.dumps(serialize(value), indent=2, default=str))


def echo_error(exc: Exception) -> None:
    """Render an exception as JSON on stderr.

    A :class:`DigitalOceanApiError` surfaces the provider's raw error payload
    verbatim; any other exception renders as a ``{"code", "message"}`` object.
    """
    payload = (
        exc.payload
        if isinstance(exc, DigitalOceanApiError)
        else {"code": type(exc).__name__, "message": str(exc)}
    )
    typer.echo(json.dumps(payload, indent=2, default=str), err=True)
