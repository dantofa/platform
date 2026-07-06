from __future__ import annotations

import re
from pathlib import Path
from unittest.mock import MagicMock, patch

from typer.testing import CliRunner

from dantofa.cli.main import app

runner = CliRunner()
_ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")


def _plain(text: str) -> str:
    return _ANSI.sub("", text)


def test_create_reports_registry_endpoints():
    instance = MagicMock()
    instance.list.return_value = []
    with patch("dantofa.commands.local.clusters.KindClient", return_value=instance):
        result = runner.invoke(
            app,
            ["local", "cluster", "create", "dev", "--registry-port", "5001"],
        )
    assert result.exit_code == 0
    out = _plain(result.stdout)
    assert '"registry": "localhost:5001"' in out
    assert '"registry_in_cluster": "kind-registry:5000"' in out
    instance.create.assert_called_once()


def test_list_renders_json():
    instance = MagicMock()
    instance.list.return_value = ["dev"]
    with patch("dantofa.commands.local.clusters.KindClient", return_value=instance):
        result = runner.invoke(app, ["local", "cluster", "list"])
    assert result.exit_code == 0
    assert '"dev"' in _plain(result.stdout)


def test_connect_writes_kubeconfig(tmp_path: Path):
    instance = MagicMock()
    instance.list.return_value = ["dev"]
    instance.get_kubeconfig.return_value = "apiVersion: v1\n"
    out = tmp_path / "kc"
    with patch("dantofa.commands.local.clusters.KindClient", return_value=instance):
        result = runner.invoke(
            app, ["local", "cluster", "connect", "dev", "-o", str(out)]
        )
    assert result.exit_code == 0
    assert out.read_text(encoding="utf-8") == "apiVersion: v1\n"
    assert (out.stat().st_mode & 0o777) == 0o600


def test_push_publishes_and_reconciles():
    instance = MagicMock()
    instance.git_provenance.return_value = ("src", "rev")
    with patch("dantofa.commands.local.clusters.KindClient", return_value=instance):
        result = runner.invoke(
            app,
            ["local", "cluster", "push", "--registry-port", "5001"],
        )
    assert result.exit_code == 0
    instance.push_artifact.assert_called_once()
    instance.reconcile_source.assert_called_once_with("local")  # always reconciles
    assert '"artifact": "localhost:5001/local:latest"' in _plain(result.stdout)


def test_delete_absent_is_idempotent():
    instance = MagicMock()
    instance.list.return_value = []
    with patch("dantofa.commands.local.clusters.KindClient", return_value=instance):
        result = runner.invoke(app, ["local", "cluster", "delete", "gone"])
    assert result.exit_code == 0
    instance.delete.assert_not_called()
