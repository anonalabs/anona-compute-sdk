"""The capacity wait on create and connect, against the fake gateway.

``retries=0`` turns off the E2B SDK's own short retry, so every 503 reaches
the wait and the fake clock below stands in for real sleeps. The jitter stays
random except where a test depends on its value.
"""

import pytest

import anona_compute
from anona_compute import _capacity
from fake_gateway import API_KEY

CREATE = "/v2/sandboxes"
CAPACITY = {
    "code": 503,
    "error_code": "sandbox_capacity_unavailable",
    "message": "no sandbox capacity is free right now; retry in 7s",
}
RETRY = {"Should-Retry": "true", "Retry-After": "7"}


class Clock:
    def __init__(self):
        self.now = 0.0
        self.sleeps = []

    def monotonic(self):
        return self.now

    def sleep(self, d):
        self.sleeps.append(d)
        self.now += d


@pytest.fixture
def clock(monkeypatch):
    c = Clock()
    monkeypatch.setattr(_capacity, "_monotonic", c.monotonic)
    monkeypatch.setattr(_capacity, "_sleep", c.sleep)
    monkeypatch.setitem(_capacity._defaults, "wait", None)
    monkeypatch.setitem(_capacity._defaults, "on_wait", None)
    monkeypatch.delenv(_capacity.WAIT_ENV, raising=False)
    return c


@pytest.fixture
def key(monkeypatch):
    monkeypatch.setenv("ANONA_API_KEY", API_KEY)


def create(gateway, **kw):
    return anona_compute.Sandbox.create(api_url=gateway.url, retries=0, **kw)


def test_waits_out_capacity_then_creates(gateway, clock, key):
    gateway.fail(CREATE, 503, CAPACITY, RETRY, times=2)
    seen = []
    sbx = create(gateway, on_capacity_wait=seen.append)
    assert sbx.sandbox_id == "sbx0001"
    assert gateway.posts(CREATE) == 3
    assert len(clock.sleeps) == 2
    assert all(6.3 <= d <= 7.7 for d in clock.sleeps), clock.sleeps  # Retry-After, 10% jitter
    assert [w.attempt for w in seen] == [1, 2]
    assert seen[0].error_code == "sandbox_capacity_unavailable"
    assert seen[0].status == 503
    assert "no sandbox capacity" in seen[0].message


def test_retry_after_is_held_between_1_and_30_seconds(gateway, clock, key):
    gateway.fail(CREATE, 503, CAPACITY, {"Should-Retry": "true", "Retry-After": "600"})
    gateway.fail(CREATE, 503, CAPACITY, {"Should-Retry": "true", "Retry-After": "0"})
    gateway.fail(CREATE, 503, CAPACITY, {"Should-Retry": "true"})
    create(gateway)
    assert gateway.posts(CREATE) == 4
    big, small, missing = clock.sleeps
    assert 27 <= big <= 30
    assert 1 <= small <= 1.1
    assert 4.5 <= missing <= 5.5  # no Retry-After: 5 s


# How many tries fit in the wait depends on the jitter, so it is pinned: at the
# low end a fourth full wait still fits before the deadline, and with
# random.random about one run in twelve failed on the count.
@pytest.mark.parametrize(
    "jitter,waits,tries",
    [
        (0, [6.3, 6.3, 6.3, 1.1], 5),  # tries at 0, 6.3, 12.6, 18.9 and 20 s
        (0.5, [7, 7, 6], 4),  # tries at 0, 7, 14 and 20 s
        (0.999, [7.6986, 7.6986, 4.6028], 4),  # tries at 0, 7.7, 15.4 and 20 s
    ],
)
def test_deadline_raises_the_original_error(gateway, clock, key, monkeypatch, jitter, waits, tries):
    monkeypatch.setattr(_capacity, "_random", lambda: jitter)
    gateway.fail(CREATE, 503, CAPACITY, RETRY, times=100)
    with pytest.raises(Exception, match="no sandbox capacity is free") as err:
        create(gateway, wait_for_capacity=20)
    assert "503" in str(err.value)
    # every wait but the last is the jittered Retry-After; the last is cut to
    # what is left of the 20 s, and the try after it is the last one
    assert clock.sleeps == pytest.approx(waits)
    assert sum(clock.sleeps) == pytest.approx(20)
    assert gateway.posts(CREATE) == tries


def test_zero_turns_the_wait_off(gateway, clock, key):
    gateway.fail(CREATE, 503, CAPACITY, RETRY, times=5)
    with pytest.raises(Exception, match="503"):
        create(gateway, wait_for_capacity=0)
    assert gateway.posts(CREATE) == 1
    assert clock.sleeps == []


def test_configure_and_env_set_the_default(gateway, clock, key, monkeypatch):
    gateway.fail(CREATE, 503, CAPACITY, RETRY, times=5)
    monkeypatch.setenv(_capacity.WAIT_ENV, "0")
    with pytest.raises(Exception, match="503"):
        create(gateway)
    assert gateway.posts(CREATE) == 1

    monkeypatch.setenv(_capacity.WAIT_ENV, "10m")
    anona_compute.configure(wait_for_capacity=0)  # configure() wins over the environment
    with pytest.raises(Exception, match="503"):
        create(gateway)
    assert gateway.posts(CREATE) == 2

    create(gateway, wait_for_capacity=600)  # the call wins over configure()
    assert gateway.posts(CREATE) == 6


@pytest.mark.parametrize(
    "status,body,headers",
    [
        (
            403,
            {"code": 403, "error_code": "plan_limit_exceeded", "message": "plan limit", "limit_name": "templates"},
            {"Should-Retry": "false"},
        ),
        (
            429,
            {"code": 429, "error_code": "plan_limit_exceeded", "message": "too many at once"},
            {"Should-Retry": "false"},
        ),
        (503, CAPACITY, {"Should-Retry": "false", "Retry-After": "7"}),
        (503, CAPACITY, {"Retry-After": "7"}),  # no Should-Retry: a proxy answered, outcome unknown
        (500, {"code": 500, "message": "sandbox engine error"}, {}),
    ],
)
def test_other_errors_are_not_retried(gateway, clock, key, status, body, headers):
    gateway.fail(CREATE, status, body, headers, times=5)
    with pytest.raises(Exception):
        create(gateway)
    assert gateway.posts(CREATE) == 1
    assert clock.sleeps == []


def test_takes_over_when_the_e2b_retries_run_out(gateway, clock, key):
    # e2b retries 3 times itself (about 3 s here, real time), then raises.
    gateway.fail(CREATE, 503, CAPACITY, {"Should-Retry": "true", "Retry-After": "1"}, times=4)
    sbx = anona_compute.Sandbox.create(api_url=gateway.url)
    assert sbx.sandbox_id == "sbx0001"
    assert gateway.posts(CREATE) == 5
    assert len(clock.sleeps) == 1


def test_connect_waits_for_capacity(gateway, clock, key):
    sbx = create(gateway)
    path = f"/v2/sandboxes/{sbx.sandbox_id}/connect"
    gateway.fail(path, 503, dict(CAPACITY, error_code="sandbox_engine_starting"), RETRY)
    seen = []
    again = anona_compute.Sandbox.connect(
        sbx.sandbox_id, api_url=gateway.url, retries=0, on_capacity_wait=seen.append
    )
    assert again.sandbox_id == sbx.sandbox_id
    assert gateway.posts(path) == 2
    assert [w.error_code for w in seen] == ["sandbox_engine_starting"]

    gateway.fail(path, 503, CAPACITY, RETRY)
    assert sbx.connect(retries=0) is sbx  # the instance form resumes the same way
    assert gateway.posts(path) == 4

    gateway.fail(path, 503, CAPACITY, RETRY)
    with pytest.raises(Exception, match="503"):
        sbx.connect(retries=0, wait_for_capacity=0)


def test_parse_duration():
    assert _capacity.parse_duration("600") == 600
    assert _capacity.parse_duration("10m") == 600
    assert _capacity.parse_duration("1m30s") == 90
    assert _capacity.parse_duration("0") == 0
    assert _capacity.parse_duration("soon") is None
    assert _capacity.parse_duration("") is None
