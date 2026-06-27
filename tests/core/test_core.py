from importlib.metadata import version

from dantofa.core import meta


def test_resolve_version_matches_metadata():
    assert meta.resolve_version() == version("dantofa-cli")
