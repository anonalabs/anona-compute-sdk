import pytest
from e2b.exceptions import AuthenticationException, InvalidArgumentException, NotFoundException

import anona_compute
from fake_gateway import API_KEY


def test_create_returns_anona_sandbox(sandbox, gateway):
    assert isinstance(sandbox, anona_compute.Sandbox)
    method, path, headers, _ = gateway.requests[0]
    assert (method, path) == ("POST", "/v2/sandboxes")
    assert headers["x-api-key"] == API_KEY


def test_token_claims_match_request(sandbox, gateway):
    tok = sandbox.live.token(scope="control", channels=["pty", "dom"], ttl=120)
    claims = tok.claims()
    assert claims["ch"] == ["pty", "dom"]
    assert claims["scp"] == "control"
    assert claims["sbx"] == sandbox.sandbox_id
    assert claims["exp"] <= claims["iat"] + 600
    assert claims["exp"] - claims["iat"] == 120
    _, path, _, body = gateway.requests[-1]
    assert path == f"/v1/sandboxes/{sandbox.sandbox_id}/viewer-tokens"
    assert body == {"scope": "control", "channels": ["pty", "dom"], "ttlSeconds": 120}


def test_token_repr_hides_secret(sandbox):
    tok = sandbox.live.token()
    assert tok.token not in repr(tok)
    assert "embed" not in repr(tok)


def test_url_defaults_to_view_dom(sandbox, gateway):
    url = sandbox.live.url()
    assert url.startswith(f"https://stream.compute.anonalabs.com/embed/v1/{sandbox.sandbox_id}?mode=dom#t=")
    assert gateway.requests[-1][3] == {"scope": "view", "channels": ["dom"], "ttlSeconds": 600}


def test_revoke_all(sandbox, gateway):
    sandbox.live.revoke_all()
    assert gateway.revoked == [sandbox.sandbox_id]
    assert gateway.requests[-1][1].endswith("/viewer-tokens:revoke-all")


@pytest.mark.parametrize(
    "kwargs",
    [
        {"scope": "admin"},
        {"channels": []},
        {"channels": ["audio"]},
        {"ttl": 601},
        {"ttl": 29},
        {"ttl": 60.5},
        {"ttl": True},
    ],
)
def test_invalid_arguments_fail_before_any_request(sandbox, gateway, kwargs):
    before = len(gateway.requests)
    with pytest.raises(InvalidArgumentException):
        sandbox.live.token(**kwargs)
    assert len(gateway.requests) == before


def test_gateway_errors_are_mapped(gateway, monkeypatch):
    monkeypatch.setenv("ANONA_API_KEY", API_KEY)
    sbx = anona_compute.Sandbox.create(api_url=gateway.url)
    other = anona_compute.Sandbox.connect(sbx.sandbox_id, api_url=gateway.url)
    assert isinstance(other, anona_compute.Sandbox)

    other.connection_config.api_key = "e2b_" + "00" * 20
    with pytest.raises(AuthenticationException):
        other.live.token()

    gateway.sandboxes.clear()
    with pytest.raises(NotFoundException):
        sbx.live.token()
