"""Acceptance tests against a live gateway.

Runs only with GATEWAY_URL and GATEWAY_API_KEY set (``pytest -m gateway``).
"""

import os

import e2b
import pytest

import anona_compute

pytestmark = [
    pytest.mark.gateway,
    pytest.mark.skipif(
        not (os.getenv("GATEWAY_URL") and os.getenv("GATEWAY_API_KEY")),
        reason="needs GATEWAY_URL and GATEWAY_API_KEY (a live gateway)",
    ),
]


def _roundtrip(sbx):
    try:
        assert sbx.commands.run("echo ok").stdout.strip() == "ok"
        sbx.files.write("/tmp/anona.txt", "hello")
        assert sbx.files.read("/tmp/anona.txt") == "hello"
    finally:
        sbx.kill()


def test_anona_sandbox(monkeypatch):
    monkeypatch.setenv("ANONA_API_KEY", os.environ["GATEWAY_API_KEY"])
    _roundtrip(anona_compute.Sandbox.create(api_url=os.environ["GATEWAY_URL"]))


def test_stock_sdk_with_e2b_env_only(monkeypatch):
    monkeypatch.setenv("E2B_API_KEY", os.environ["GATEWAY_API_KEY"])
    monkeypatch.setenv("E2B_API_URL", os.environ["GATEWAY_URL"])
    _roundtrip(e2b.Sandbox.create())
