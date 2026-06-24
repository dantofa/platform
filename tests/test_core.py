from importlib.metadata import version

from dantofa.core import greeting, meta


def test_greet():
    assert greeting.greet("dantofa") == "Hello, dantofa!"


def test_resolve_version_matches_metadata():
    assert meta.resolve_version() == version("dantofa-cli")
