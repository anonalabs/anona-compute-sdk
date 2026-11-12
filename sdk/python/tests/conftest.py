import os
import sys

import pytest

sys.path.insert(0, os.path.dirname(__file__))

from fake_gateway import API_KEY, FakeGateway  # noqa: E402

_E2B_VARS = ("E2B_API_KEY", "E2B_DOMAIN", "E2B_API_URL", "E2B_DEBUG", "E2B_SANDBOX_URL")
_ANONA_VARS = ("ANONA_API_KEY", "ANONA_DOMAIN", "ANONA_API_URL")


@pytest.fixture(autouse=True)
def clean_env(monkeypatch):
    for name in _E2B_VARS + _ANONA_VARS:
        monkeypatch.delenv(name, raising=False)


@pytest.fixture
def gateway():
    with FakeGateway() as gw:
        yield gw


@pytest.fixture
def sandbox(gateway, monkeypatch):
    import anona_compute

    monkeypatch.setenv("ANONA_API_KEY", API_KEY)
    sbx = anona_compute.Sandbox.create(api_url=gateway.url)
    return sbx
