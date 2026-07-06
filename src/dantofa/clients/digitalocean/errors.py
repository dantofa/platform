"""Exceptions for the DigitalOcean client adapters.

These live in the clients layer so SDK-specific errors (pydo's azure-core
``HttpResponseError``, botocore's ``ClientError``) are translated at this
boundary into project exceptions. Callers above (core, commands) catch these and
never import the SDKs' error types.
"""

from __future__ import annotations


class MissingCredentialsError(RuntimeError):
    """No API token / Spaces keys were supplied via argument or environment."""


class DigitalOceanApiError(RuntimeError):
    """An API call to DigitalOcean (REST or Spaces/S3) failed.

    Carries the provider's raw error ``payload`` — the parsed JSON body for the
    DO REST API, or botocore's error ``response`` dict for Spaces/S3 — so the CLI
    can surface it to the user verbatim instead of a reworded summary.
    """

    def __init__(self, payload: object) -> None:
        self.payload: object = payload
        super().__init__(str(payload))
