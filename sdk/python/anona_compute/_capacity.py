"""Wait for capacity: retry a sandbox create or connect while the API says to.

When every host is full the API answers a create with ``503``,
``Should-Retry: true`` and ``Retry-After`` while it starts another host, which
takes a few minutes. The E2B SDK retries such an answer a few times within one
request timeout (a minute by default) and then raises. This module keeps
retrying, after each ``Retry-After``, until a deadline: 10 minutes unless the
caller says otherwise, ``0`` to turn it off.

Only a ``503`` marked ``Should-Retry: true`` is retried. Every other error,
including a ``503`` with ``Should-Retry: false`` or without the header (an
answer from something in front of the API, after which a sandbox may already
have started) and every plan limit (``403`` / ``429`` ``plan_limit_exceeded``),
is raised at once.

The E2B SDK's exceptions carry neither the response headers nor the body's
``error_code``, so ``install()`` wraps the function that builds them,
``e2b.sandbox_sync.sandbox_api.handle_api_exception``, to attach what the
response said. The wrapper returns the same exception it always did; when it
cannot be installed the wait is off and errors are raised as before.
"""

from __future__ import annotations

import json
import logging
import os
import random
import re
import time
from dataclasses import dataclass
from typing import Any, Callable, Mapping, Optional, TypeVar

DEFAULT_WAIT_FOR_CAPACITY = 600.0
"""Seconds a create or connect keeps retrying by default: long enough for a
host to start (about 2 to 4 minutes) with room to spare."""

WAIT_ENV = "ANONA_COMPUTE_CAPACITY_WAIT"
"""Environment variable with the default wait, the same one the CLI reads:
seconds (``600``) or a duration (``10m``, ``90s``); ``0`` turns the wait off."""

MIN_DELAY = 1.0
MAX_DELAY = 30.0
DEFAULT_DELAY = 5.0
"""Wait between tries when the answer has no usable ``Retry-After``."""

log = logging.getLogger("anona_compute")

_ATTR = "_anona_refusal"


@dataclass(frozen=True)
class CapacityWait:
    """What ``on_capacity_wait`` is told before each wait."""

    attempt: int
    """1 for the first retry, 2 for the second, and so on."""
    delay: float
    """Seconds until the next try."""
    elapsed: float
    """Seconds since the first try."""
    remaining: float
    """Seconds left before the wait gives up."""
    status: int
    """HTTP status of the refusal (503)."""
    error_code: Optional[str]
    """The API's ``error_code``, e.g. ``sandbox_capacity_unavailable``."""
    message: str
    """The API's message."""


OnCapacityWait = Callable[[CapacityWait], None]


@dataclass(frozen=True)
class Refusal:
    """What an API error response said, attached to the exception raised for it."""

    status: int
    should_retry: Optional[str]
    retry_after: Optional[str]
    error_code: Optional[str]
    message: str

    @property
    def retryable(self) -> bool:
        return self.status == 503 and (self.should_retry or "").strip().lower() == "true"


_defaults: dict[str, Any] = {"wait": None, "on_wait": None}


def set_defaults(wait: Optional[float] = None, on_wait: Optional[OnCapacityWait] = None) -> None:
    """Process-wide defaults, set by ``anona_compute.configure``."""
    if wait is not None:
        if wait < 0:
            raise ValueError("wait_for_capacity must be 0 or more seconds")
        _defaults["wait"] = float(wait)
    if on_wait is not None:
        _defaults["on_wait"] = on_wait


_DURATION = re.compile(r"(\d+(?:\.\d+)?)(ms|s|m|h)")
_UNIT = {"ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}


def parse_duration(value: str) -> Optional[float]:
    """Seconds in ``value``: a number of seconds or a Go-style duration (``1m30s``)."""
    v = value.strip()
    if not v:
        return None
    try:
        return max(float(v), 0.0)
    except ValueError:
        pass
    parts = _DURATION.findall(v)
    if not parts or "".join(n + u for n, u in parts) != v:
        return None
    return sum(float(n) * _UNIT[u] for n, u in parts)


def resolve_wait(per_call: Optional[float]) -> float:
    """The wait for one call: the argument, else ``configure()``, else the
    environment, else 10 minutes."""
    if per_call is not None:
        if per_call < 0:
            raise ValueError("wait_for_capacity must be 0 or more seconds")
        return float(per_call)
    if _defaults["wait"] is not None:
        return _defaults["wait"]
    env = parse_duration(os.environ.get(WAIT_ENV, ""))
    return DEFAULT_WAIT_FOR_CAPACITY if env is None else env


def resolve_on_wait(per_call: Optional[OnCapacityWait]) -> Optional[OnCapacityWait]:
    return per_call if per_call is not None else _defaults["on_wait"]


def _header(headers: Any, name: str) -> Optional[str]:
    if headers is None:
        return None
    try:
        v = headers.get(name)
        if v is None and isinstance(headers, Mapping):
            low = name.lower()
            v = next((val for k, val in headers.items() if str(k).lower() == low), None)
        return None if v is None else str(v)
    except Exception:
        return None


def refusal_from_response(res: Any) -> Refusal:
    """Read status, ``Should-Retry``, ``Retry-After``, ``error_code`` and
    message from an E2B API response."""
    headers = getattr(res, "headers", None)
    body: dict = {}
    try:
        content = getattr(res, "content", b"") or b""
        parsed = json.loads(content)
        if isinstance(parsed, dict):
            body = parsed
    except (ValueError, TypeError):
        pass
    code = body.get("error_code")
    return Refusal(
        status=int(getattr(res, "status_code", 0) or 0),
        should_retry=_header(headers, "Should-Retry"),
        retry_after=_header(headers, "Retry-After"),
        error_code=code if isinstance(code, str) else None,
        message=str(body.get("message") or ""),
    )


def refusal_of(err: BaseException) -> Optional[Refusal]:
    r = getattr(err, _ATTR, None)
    return r if isinstance(r, Refusal) else None


def install() -> bool:
    """Wrap the E2B SDK's API-error builder so the exception it returns carries
    the response's ``Refusal``. Idempotent; returns whether it is in place."""
    try:
        from e2b.sandbox_sync import sandbox_api
    except ImportError:  # pragma: no cover - an E2B SDK without the module
        return False
    original = getattr(sandbox_api, "handle_api_exception", None)
    if original is None:
        return False
    if getattr(original, "_anona_wrapped", False):
        return True

    def handle_api_exception(e: Any, *args: Any, **kwargs: Any) -> Any:
        err = original(e, *args, **kwargs)
        try:
            setattr(err, _ATTR, refusal_from_response(e))
        except Exception:  # never let the annotation change what is raised
            pass
        return err

    handle_api_exception._anona_wrapped = True  # type: ignore[attr-defined]
    sandbox_api.handle_api_exception = handle_api_exception
    return True


def retry_delay(r: Refusal, rand: Callable[[], float] = random.random) -> float:
    """``Retry-After`` held between 1 and 30 seconds, with up to 10% jitter so
    many waiting callers do not all come back at once."""
    try:
        base = float((r.retry_after or "").strip())
    except ValueError:
        base = DEFAULT_DELAY
    if base != base:  # NaN
        base = DEFAULT_DELAY
    base = min(max(base, MIN_DELAY), MAX_DELAY)
    return min(max(base * (0.9 + 0.2 * rand()), MIN_DELAY), MAX_DELAY)


# Seams for tests. ``_random`` is the jitter's source, in [0, 1).
_sleep: Callable[[float], None] = time.sleep
_monotonic: Callable[[], float] = time.monotonic
_random: Callable[[], float] = random.random

T = TypeVar("T")


def call(
    fn: Callable[[], T],
    *,
    what: str,
    wait: Optional[float] = None,
    on_wait: Optional[OnCapacityWait] = None,
) -> T:
    """Run ``fn``, retrying while it raises a ``503`` marked ``Should-Retry:
    true``, until ``wait`` seconds have passed. The last error is raised
    unchanged once the wait runs out."""
    budget = resolve_wait(wait)
    callback = resolve_on_wait(on_wait)
    if budget <= 0:
        return fn()
    start = _monotonic()
    attempt = 0
    while True:
        try:
            return fn()
        except Exception as err:
            r = refusal_of(err)
            if r is None or not r.retryable:
                raise
            elapsed = _monotonic() - start
            remaining = budget - elapsed
            if remaining <= 0:
                raise
            attempt += 1
            delay = min(retry_delay(r, _random), remaining)
            info = CapacityWait(
                attempt=attempt,
                delay=delay,
                elapsed=elapsed,
                remaining=remaining,
                status=r.status,
                error_code=r.error_code,
                message=r.message,
            )
            log.info(
                "%s: %s (%s); retrying in %.0fs, %.0fs of %.0fs waited",
                what,
                _why(r.error_code),
                r.error_code or r.status,
                delay,
                elapsed,
                budget,
            )
            if callback is not None:
                callback(info)
            _sleep(delay)


def _why(code: Optional[str]) -> str:
    if code == "sandbox_capacity_unavailable":
        return "no sandbox capacity is free; a host may be starting"
    if code == "sandbox_engine_starting":
        return "the sandbox server is starting"
    if code == "sandbox_size_preparing":
        return "the template is being prepared at this size"
    return "the service is not ready yet"
