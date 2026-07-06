from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest
from botocore.exceptions import ClientError

from dantofa.clients.digitalocean import spaces as adapter
from dantofa.clients.digitalocean.errors import (
    DigitalOceanApiError,
    MissingCredentialsError,
)


def _client_error(code: str) -> ClientError:
    return ClientError({"Error": {"Code": code}}, "CreateBucket")


def test_create_delegates():
    s3 = MagicMock()
    with patch("boto3.client", return_value=s3):
        client = adapter.SpacesClient(region="nyc3", key_id="k", secret="s")
        assert client.create("b") is None
    s3.create_bucket.assert_called_once_with(Bucket="b")


def test_create_error_surfaces_raw_response():
    s3 = MagicMock()
    s3.create_bucket.side_effect = _client_error("BucketAlreadyOwnedByYou")
    with patch("boto3.client", return_value=s3):
        client = adapter.SpacesClient(region="nyc3", key_id="k", secret="s")
        with pytest.raises(DigitalOceanApiError) as excinfo:
            _ = client.create("b")
    assert excinfo.value.payload == {"Error": {"Code": "BucketAlreadyOwnedByYou"}}


def test_missing_credentials_raises(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.delenv(adapter.KEY_ENV, raising=False)
    monkeypatch.delenv(adapter.SECRET_ENV, raising=False)
    with pytest.raises(MissingCredentialsError):
        _ = adapter.SpacesClient(region="nyc3")


def test_endpoint_built_from_region():
    with patch("boto3.client") as boto:
        _ = adapter.SpacesClient(region="ams3", key_id="k", secret="s")
    _, kwargs = boto.call_args
    assert kwargs["endpoint_url"] == "https://ams3.digitaloceanspaces.com"
    assert kwargs["region_name"] == "ams3"


def test_list_and_delete():
    s3 = MagicMock()
    s3.list_buckets.return_value = {"Buckets": [{"Name": "b"}]}
    with patch("boto3.client", return_value=s3):
        client = adapter.SpacesClient(region="nyc3", key_id="k", secret="s")
        assert client.list() == [{"Name": "b"}]
        client.delete("b")
    s3.delete_bucket.assert_called_once_with(Bucket="b")
