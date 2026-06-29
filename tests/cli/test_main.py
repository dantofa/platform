import re
from importlib.metadata import version

from typer.testing import CliRunner

from dantofa.cli.main import app

runner = CliRunner()

# Handle rich's color output
_ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")


def _plain(text: str) -> str:
    return _ANSI.sub("", text)


def test_version():
    result = runner.invoke(app, ["--version"])
    assert result.exit_code == 0
    assert version("dantofa-saas") in _plain(result.stdout)


def test_help_lists_do_group():
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "do" in _plain(result.stdout)


def test_digitalocean_alias_mirrors_do():
    result = runner.invoke(app, ["digitalocean", "cluster", "--help"])
    assert result.exit_code == 0
    assert "list" in _plain(result.stdout)
