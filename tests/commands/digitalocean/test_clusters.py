from __future__ import annotations

import re
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from dantofa.cli.main import app

runner = CliRunner()
_ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")


def _plain(text: str) -> str:
    return _ANSI.sub("", text)


def test_list_renders_json():
    instance = MagicMock()
    instance.list.return_value = [{"id": "1", "name": "c"}]
    with patch(
        "dantofa.commands.digitalocean.clusters.ClusterClient",
        return_value=instance,
    ):
        result = runner.invoke(app, ["do", "cluster", "list", "--token", "t"])
    assert result.exit_code == 0
    assert '"name": "c"' in _plain(result.stdout)


def test_create_defaults_region_and_version():
    instance = MagicMock()
    instance.create.return_value = {"id": "new", "name": "saas-preview-1"}
    with patch(
        "dantofa.commands.digitalocean.clusters.ClusterClient",
        return_value=instance,
    ):
        result = runner.invoke(
            app,
            ["do", "cluster", "create", "--name", "saas-preview-1", "--token", "t"],
        )
    assert result.exit_code == 0
    body = instance.create.call_args.args[0]
    assert body["region"] == "nyc3"
    assert body["version"] == "latest"
    # Locked-down invariants: system pool, autoscaling, auto/surge upgrade on.
    assert body["node_pools"][0]["name"] == "system"
    assert body["node_pools"][0]["auto_scale"] is True
    assert body["auto_upgrade"] is True
    assert body["surge_upgrade"] is True
    assert '"id": "new"' in _plain(result.stdout)


def test_create_requires_name():
    result = runner.invoke(app, ["do", "cluster", "create", "--token", "t"])
    assert result.exit_code != 0


def test_update_reasserts_invariants_and_sets_ha():
    instance = MagicMock()
    instance.list.return_value = [{"id": "abc", "name": "c1"}]
    instance.update.return_value = {"id": "abc", "name": "c1"}
    with patch(
        "dantofa.commands.digitalocean.clusters.ClusterClient",
        return_value=instance,
    ):
        result = runner.invoke(
            app,
            ["do", "cluster", "update", "c1", "--ha", "--token", "t"],
        )
    assert result.exit_code == 0
    cluster_id, body = instance.update.call_args.args
    assert cluster_id == "abc"
    # name preserved (no rename), upgrade invariants re-asserted, ha applied.
    assert body == {
        "name": "c1",
        "auto_upgrade": True,
        "surge_upgrade": True,
        "ha": True,
    }


def test_update_clear_tags_sends_empty_list():
    instance = MagicMock()
    instance.list.return_value = [{"id": "abc", "name": "c1"}]
    instance.update.return_value = {"id": "abc", "name": "c1"}
    with patch(
        "dantofa.commands.digitalocean.clusters.ClusterClient",
        return_value=instance,
    ):
        result = runner.invoke(
            app,
            ["do", "cluster", "update", "c1", "--clear-tags", "--token", "t"],
        )
    assert result.exit_code == 0
    _, body = instance.update.call_args.args
    assert body["tags"] == []


def test_update_clear_tags_conflicts_with_tag():
    with patch("dantofa.commands.digitalocean.clusters.ClusterClient"):
        result = runner.invoke(
            app,
            [
                "do",
                "cluster",
                "update",
                "c1",
                "--tag",
                "a",
                "--clear-tags",
                "--token",
                "t",
            ],
        )
    assert result.exit_code != 0


def test_delete_resolves_and_deletes():
    instance = MagicMock()
    instance.list.return_value = [{"id": "abc", "name": "c1"}]
    with patch(
        "dantofa.commands.digitalocean.clusters.ClusterClient",
        return_value=instance,
    ):
        result = runner.invoke(app, ["do", "cluster", "delete", "c1", "--token", "t"])
    assert result.exit_code == 0
    instance.delete.assert_called_once_with("abc")


def test_delete_absent_succeeds():
    instance = MagicMock()
    instance.list.return_value = []  # nothing to delete
    with patch(
        "dantofa.commands.digitalocean.clusters.ClusterClient",
        return_value=instance,
    ):
        result = runner.invoke(app, ["do", "cluster", "delete", "gone", "--token", "t"])
    assert result.exit_code == 0  # idempotent: absent is success
    instance.delete.assert_not_called()


def test_connect_writes_kubeconfig(tmp_path: Path):
    instance = MagicMock()
    instance.list.return_value = [{"id": "abc", "name": "c1"}]
    instance.get_kubeconfig.return_value = "apiVersion: v1\n"
    out = tmp_path / "kc.yaml"
    with patch(
        "dantofa.commands.digitalocean.clusters.ClusterClient",
        return_value=instance,
    ):
        result = runner.invoke(
            app,
            ["do", "cluster", "connect", "c1", "-o", str(out), "--token", "t"],
        )
    assert result.exit_code == 0
    instance.get_kubeconfig.assert_called_once_with("abc")
    assert out.read_text(encoding="utf-8") == "apiVersion: v1\n"
    assert (out.stat().st_mode & 0o777) == 0o600


def test_api_error_renders_raw_payload():
    from dantofa.clients.digitalocean.errors import DigitalOceanApiError

    instance = MagicMock()
    instance.list.side_effect = DigitalOceanApiError(
        {"id": "unprocessable_entity", "message": "version is invalid"},
    )
    with patch(
        "dantofa.commands.digitalocean.clusters.ClusterClient",
        return_value=instance,
    ):
        result = runner.invoke(app, ["do", "cluster", "list", "--token", "t"])
    assert result.exit_code == 1
    try:
        err = result.stderr
    except ValueError:  # older Click mixes stderr into stdout
        err = ""
    out = _plain(result.output + err)
    assert '"id": "unprocessable_entity"' in out
    assert '"message": "version is invalid"' in out
    # raw payload, not the {code, message} wrapper
    assert "DigitalOceanApiError" not in out


def test_missing_credentials_exits_nonzero(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.delenv("DIGITALOCEAN_ACCESS_TOKEN", raising=False)
    result = runner.invoke(app, ["do", "cluster", "list"])
    assert result.exit_code == 1
