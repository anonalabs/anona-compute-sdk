import pytest

e2b_desktop = pytest.importorskip("e2b_desktop")


def test_desktop_sandbox_extends_e2b_desktop():
    import anona_compute.desktop

    assert issubclass(anona_compute.desktop.Sandbox, e2b_desktop.Sandbox)
    for name in ("live", "ssh", "idle_pause", "live_url"):
        assert hasattr(anona_compute.desktop.Sandbox, name)


def test_live_url_mints_a_control_desktop_token(sandbox, gateway):
    import anona_compute.desktop

    # e2b_desktop.Sandbox.create starts Xvfb through envd, which the fake
    # gateway does not serve, so call the method on a plain anona_compute.Sandbox.
    url = anona_compute.desktop.Sandbox.live_url(sandbox, ttl=120)
    _, path, _, body = gateway.requests[-1]
    assert path == f"/v1/sandboxes/{sandbox.sandbox_id}/viewer-tokens"
    assert body == {"scope": "control", "channels": ["desktop"], "ttlSeconds": 120}
    assert "#t=" in url
    anona_compute.desktop.Sandbox.live_url(sandbox, control=False)
    assert gateway.requests[-1][3]["scope"] == "view"
