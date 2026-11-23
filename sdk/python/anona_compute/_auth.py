"""A missing API key names Anona's dashboard, not E2B's.

The E2B SDK refuses to build its API client without a key, with a message that
sends the user to ``https://e2b.dev/dashboard`` for one. Against Anona Compute
that is the wrong place: keys come from the Anona dashboard. This rewrites that
one refusal when the client is pointed anywhere but E2B's own cloud; every
other error, and a client pointed at E2B, is left as it is.
"""

from __future__ import annotations

import functools

from e2b.exceptions import AuthenticationException

KEYS_URL = "https://compute.anonalabs.com"
"""Where an Anona Compute API key is created (the dashboard's API keys page)."""

MISSING_KEY_MESSAGE = (
    f"API key is required: create one in the Anona Compute dashboard at {KEYS_URL} (API keys). "
    "Set the environment variable `ANONA_API_KEY` (or `E2B_API_KEY`), call "
    'anona_compute.configure(api_key="ac_..."), or pass api_key="ac_..." to the method.'
)

_E2B_DOMAINS = ("e2b.app", "e2b.dev")


def _is_e2b_cloud(domain: str | None) -> bool:
    d = (domain or "").lower().rstrip(".")
    return any(d == e or d.endswith("." + e) for e in _E2B_DOMAINS)


def install() -> None:
    """Wrap ``e2b.api.ApiClient.__init__`` once."""
    from e2b.api import ApiClient

    init = ApiClient.__init__
    if getattr(init, "_anona_missing_key", False):
        return

    @functools.wraps(init)
    def __init__(self, config, *args, **kwargs):  # type: ignore[no-untyped-def]
        try:
            init(self, config, *args, **kwargs)
        except AuthenticationException as e:
            if "e2b.dev/dashboard" in str(e) and not _is_e2b_cloud(getattr(config, "domain", None)):
                raise AuthenticationException(MISSING_KEY_MESSAGE) from None
            raise

    __init__._anona_missing_key = True  # type: ignore[attr-defined]
    ApiClient.__init__ = __init__  # type: ignore[method-assign]
