"""Exceptions for the local (kind) client adapter."""

from __future__ import annotations


class LocalClusterError(RuntimeError):
    """A local tool (kind/docker/kubectl) invocation failed or is unavailable."""
