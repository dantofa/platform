"""Package metadata logic. No CLI/framework concerns live here."""

from importlib.metadata import PackageNotFoundError, version

_DISTRIBUTION = "dantofa-saas"


def resolve_version() -> str:
    """Return the installed package version, or a sentinel when not installed."""
    try:
        return version(_DISTRIBUTION)
    except PackageNotFoundError:  # running from a source tree without install
        return "0.0.0+unknown"
