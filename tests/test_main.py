from importlib.metadata import version

from typer.testing import CliRunner

from dantofa.cli.main import app

runner = CliRunner()


def test_version():
    result = runner.invoke(app, ["--version"])
    assert result.exit_code == 0
    assert version("dantofa-cli") in result.stdout


def test_hello_default():
    result = runner.invoke(app)
    assert result.exit_code == 0
    assert "Hello, world!" in result.stdout


def test_hello_with_name():
    result = runner.invoke(app, ["--name", "dantofa"])
    assert result.exit_code == 0
    assert "Hello, dantofa!" in result.stdout


def test_help():
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "--name" in result.stdout
