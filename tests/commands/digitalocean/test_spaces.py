from __future__ import annotations

import re
from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from dantofa.cli.main import app

runner = CliRunner()
_ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")


def _plain(text: str) -> str:
    return _ANSI.sub("", text)


def test_create_renders_created():
    instance = MagicMock()
    instance.create.return_value = None
    with patch(
        "dantofa.commands.digitalocean.spaces.SpacesClient",
        return_value=instance,
    ):
        result = runner.invoke(app, ["do", "spaces", "create", "b", "--region", "nyc3"])
    assert result.exit_code == 0
    assert '"created": "b"' in _plain(result.stdout)
    instance.create.assert_called_once_with("b")


def test_list_renders_json():
    instance = MagicMock()
    instance.list.return_value = [{"Name": "b"}]
    with patch(
        "dantofa.commands.digitalocean.spaces.SpacesClient",
        return_value=instance,
    ):
        result = runner.invoke(app, ["do", "spaces", "list", "--region", "nyc3"])
    assert result.exit_code == 0
    assert '"Name": "b"' in _plain(result.stdout)


def test_delete_renders_name():
    instance = MagicMock()
    with patch(
        "dantofa.commands.digitalocean.spaces.SpacesClient",
        return_value=instance,
    ):
        result = runner.invoke(app, ["do", "spaces", "delete", "b", "--region", "nyc3"])
    assert result.exit_code == 0
    assert '"deleted": "b"' in _plain(result.stdout)
    instance.delete.assert_called_once_with("b")


def test_missing_credentials_exits_nonzero(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.delenv("SPACES_ACCESS_KEY_ID", raising=False)
    monkeypatch.delenv("SPACES_SECRET_ACCESS_KEY", raising=False)
    result = runner.invoke(app, ["do", "spaces", "list", "--region", "nyc3"])
    assert result.exit_code == 1
