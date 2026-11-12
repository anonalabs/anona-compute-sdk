import os

import e2b
from e2b.connection_config import ConnectionConfig

import anona_compute
from anona_compute._config import apply_env


def test_defaults_point_at_anona_domain():
    params = anona_compute.Sandbox._resolve_api_params()
    cfg = ConnectionConfig(**params)
    assert cfg.domain == "compute.anonalabs.com"
    assert cfg.api_url == "https://api.compute.anonalabs.com"
    assert anona_compute.DEFAULT_API_URL == cfg.api_url


def test_anona_api_key_maps_to_e2b(monkeypatch):
    monkeypatch.setenv("ANONA_API_KEY", "e2b_" + "ab" * 20)
    cfg = ConnectionConfig(**anona_compute.Sandbox._resolve_api_params())
    assert cfg.api_key == "e2b_" + "ab" * 20


def test_anona_api_key_that_is_not_a_compute_key_is_never_sent(monkeypatch):
    # ANONA_API_KEY is shared with Anona Memory; its key must not reach the compute API.
    monkeypatch.setenv("ANONA_API_KEY", "anona_mem_" + "ab" * 20)
    assert "api_key" not in anona_compute.Sandbox._resolve_api_params()
    env = {"ANONA_API_KEY": "anona_mem_" + "ab" * 20}
    apply_env(env)
    assert "E2B_API_KEY" not in env
    env = {"ANONA_API_KEY": "ac_" + "ab" * 20}
    apply_env(env)
    assert env["E2B_API_KEY"] == "ac_" + "ab" * 20


def test_e2b_variables_win(monkeypatch):
    monkeypatch.setenv("ANONA_API_KEY", "e2b_" + "ab" * 20)
    monkeypatch.setenv("E2B_API_KEY", "e2b_" + "cd" * 20)
    monkeypatch.setenv("E2B_DOMAIN", "example.test")
    cfg = ConnectionConfig(**anona_compute.Sandbox._resolve_api_params())
    assert cfg.api_key == "e2b_" + "cd" * 20
    assert cfg.domain == "example.test"


def test_per_call_params_win(monkeypatch):
    monkeypatch.setenv("ANONA_DOMAIN", "a.test")
    cfg = ConnectionConfig(**anona_compute.Sandbox._resolve_api_params(domain="b.test"))
    assert cfg.domain == "b.test"


def test_configure_sets_e2b_env_for_stock_sdk():
    anona_compute.configure(api_key="e2b_" + "ef" * 20)
    assert os.environ["E2B_API_KEY"] == "e2b_" + "ef" * 20
    assert os.environ["E2B_DOMAIN"] == "compute.anonalabs.com"
    # The stock SDK now resolves the Anona API without the extras.
    assert ConnectionConfig().api_url == "https://api.compute.anonalabs.com"
    assert e2b.Sandbox is not anona_compute.Sandbox and issubclass(anona_compute.Sandbox, e2b.Sandbox)


def test_apply_env_does_not_override_existing():
    env = {"ANONA_API_KEY": "x", "E2B_API_KEY": "y", "ANONA_DOMAIN": "d.test"}
    apply_env(env)
    assert env["E2B_API_KEY"] == "y"
    assert env["E2B_DOMAIN"] == "d.test"


def test_version_is_a_string():
    assert isinstance(anona_compute.__version__, str) and anona_compute.__version__
