from __future__ import annotations

from typing import final

from dantofa.core.digitalocean import spaces as core


@final
class FakeSpacesApi:
    def __init__(self) -> None:
        self.created: str | None = None
        self.deleted: str | None = None

    def list(self) -> list[dict[str, object]]:
        return [{"Name": "b1"}]

    def create(self, name: str) -> None:
        self.created = name

    def delete(self, name: str) -> None:
        self.deleted = name


def test_create_bucket_delegates():
    client = FakeSpacesApi()
    core.create_bucket(client, "bucket")
    assert client.created == "bucket"


def test_list_passthrough():
    assert core.list_buckets(FakeSpacesApi()) == [{"Name": "b1"}]


def test_delete():
    client = FakeSpacesApi()
    core.delete_bucket(client, "bucket")
    assert client.deleted == "bucket"
