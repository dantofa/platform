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
    assert version("dantofa-cli") in _plain(result.stdout)


def test_hello_default():
    result = runner.invoke(app)
    assert result.exit_code == 0
    assert "Hello, world!" in _plain(result.stdout)


def test_hello_with_name():
    result = runner.invoke(app, ["--name", "dantofa"])
    assert result.exit_code == 0
    assert "Hello, dantofa!" in _plain(result.stdout)


def test_help():
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "--name" in _plain(result.stdout)
