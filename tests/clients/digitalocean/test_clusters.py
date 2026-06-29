from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

from dantofa.clients.digitalocean import clusters as adapter
from dantofa.clients.digitalocean.errors import (
    DigitalOceanApiError,
    MissingCredentialsError,
)


def test_resolve_token_missing_raises(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.delenv(adapter.TOKEN_ENV, raising=False)
    with pytest.raises(MissingCredentialsError):
        _ = adapter.ClusterClient()


def test_resolve_token_from_env(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv(adapter.TOKEN_ENV, "env-token")
    with patch("pydo.Client") as client_cls:
        _ = adapter.ClusterClient()
    client_cls.assert_called_once_with(token="env-token")


def test_list_follows_pagination():
    fake = MagicMock()
    fake.kubernetes.list_clusters.side_effect = [
        {"kubernetes_clusters": [{"id": "1"}], "links": {"pages": {"next": "url"}}},
        {"kubernetes_clusters": [{"id": "2"}], "links": {}},
    ]
    with patch("pydo.Client", return_value=fake):
        client = adapter.ClusterClient(token="t")
        assert [c["id"] for c in client.list()] == ["1", "2"]


def test_list_translates_api_error():
    from azure.core.exceptions import HttpResponseError

    fake = MagicMock()
    fake.kubernetes.list_clusters.side_effect = HttpResponseError("boom")
    with patch("pydo.Client", return_value=fake):
        client = adapter.ClusterClient(token="t")
        with pytest.raises(DigitalOceanApiError):
            _ = client.list()


def test_list_error_surfaces_raw_json_payload():
    from azure.core.exceptions import HttpResponseError

    response = MagicMock()
    response.json.return_value = {
        "id": "unprocessable_entity",
        "message": "bad version",
    }
    fake = MagicMock()
    fake.kubernetes.list_clusters.side_effect = HttpResponseError(response=response)
    with patch("pydo.Client", return_value=fake):
        client = adapter.ClusterClient(token="t")
        with pytest.raises(DigitalOceanApiError) as excinfo:
            _ = client.list()
    assert excinfo.value.payload == {
        "id": "unprocessable_entity",
        "message": "bad version",
    }


def test_create_update_delete_delegate():
    fake = MagicMock()
    fake.kubernetes.create_cluster.return_value = {"id": "1", "name": "c"}
    fake.kubernetes.update_cluster.return_value = {"id": "1", "name": "c2"}
    with patch("pydo.Client", return_value=fake):
        client = adapter.ClusterClient(token="t")
        assert client.create({"name": "c"})["id"] == "1"
        assert client.update("1", {"name": "c2"})["name"] == "c2"
        client.delete("1")
    fake.kubernetes.create_cluster.assert_called_once_with(body={"name": "c"})
    fake.kubernetes.update_cluster.assert_called_once_with(
        cluster_id="1",
        body={"name": "c2"},
    )
    fake.kubernetes.delete_cluster.assert_called_once_with(cluster_id="1")
