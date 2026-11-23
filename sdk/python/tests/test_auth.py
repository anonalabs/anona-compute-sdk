import pytest
from e2b.exceptions import AuthenticationException

import anona_compute
from anona_compute._auth import KEYS_URL


def test_missing_key_points_at_the_anona_dashboard():
    with pytest.raises(AuthenticationException) as e:
        anona_compute.Sandbox.create("base", wait_for_capacity=0)
    msg = str(e.value)
    assert KEYS_URL in msg
    assert "e2b.dev" not in msg
    assert "ANONA_API_KEY" in msg


def test_missing_key_against_e2b_cloud_keeps_e2b_message(monkeypatch):
    monkeypatch.setenv("E2B_DOMAIN", "e2b.app")
    import e2b

    with pytest.raises(AuthenticationException) as e:
        e2b.Sandbox.create("base")
    assert "e2b.dev/dashboard" in str(e.value)


def test_install_is_idempotent():
    from e2b.api import ApiClient

    first = ApiClient.__init__
    anona_compute._auth.install()
    assert ApiClient.__init__ is first
