"""Environment mapping between Anona settings and the variables the E2B SDK reads.

The E2B Python SDK 2.61.0 reads these variables in ``e2b/connection_config.py``:
``E2B_API_KEY``, ``E2B_DOMAIN``, ``E2B_API_URL`` (deprecated), ``E2B_SANDBOX_URL``
(deprecated), ``E2B_DEBUG`` and ``E2B_HTTP_VERSION``. Only the first four that
matter for pointing the SDK at Anona are mapped here; no new E2B variables are
invented.
"""

from __future__ import annotations

import os
from typing import Mapping, MutableMapping, Optional

DEFAULT_DOMAIN = "compute.anonalabs.com"
"""Domain customers set as ``E2B_DOMAIN``. The SDK derives ``https://api.<domain>``."""

DEFAULT_API_URL = f"https://api.{DEFAULT_DOMAIN}"
"""Public API endpoint. Only used directly by the Anona extras."""

# Anona variable -> E2B variable. ANONA_* wins only when the E2B variable is unset,
# so a customer who already exported E2B_* keeps full control.
ENV_MAP: Mapping[str, str] = {
    "ANONA_API_KEY": "E2B_API_KEY",
    "ANONA_DOMAIN": "E2B_DOMAIN",
    "ANONA_API_URL": "E2B_API_URL",
}


COMPUTE_KEY_PREFIXES = ("ac_", "e2b_")
"""Prefixes of an Anona Compute API key (``ac_``, legacy ``e2b_``)."""


def compute_key(value: Optional[str]) -> Optional[str]:
    """``value`` if it is an Anona Compute key, else ``None``.

    ``ANONA_API_KEY`` is shared with Anona Memory, whose key must never be
    sent to the compute API. The ``anona-compute`` CLI applies the same rule.
    """
    if value and value.startswith(COMPUTE_KEY_PREFIXES):
        return value
    return None


def apply_env(environ: Optional[MutableMapping[str, str]] = None) -> None:
    """Copy ``ANONA_*`` values to the ``E2B_*`` names and default ``E2B_DOMAIN``.

    ``ANONA_API_KEY`` is copied only when it holds a compute key.
    """
    env = os.environ if environ is None else environ
    for src, dst in ENV_MAP.items():
        value = env.get(src)
        if src == "ANONA_API_KEY":
            value = compute_key(value)
        if value and not env.get(dst):
            env[dst] = value
    if not env.get("E2B_DOMAIN"):
        env["E2B_DOMAIN"] = DEFAULT_DOMAIN


def configure(
    api_key: Optional[str] = None,
    *,
    domain: Optional[str] = None,
    api_url: Optional[str] = None,
    debug: Optional[bool] = None,
) -> None:
    """Point the stock E2B SDK (and the Anona extras) at Anona Compute.

    Explicit arguments overwrite the matching ``E2B_*`` variables in this
    process. Missing values fall back to ``ANONA_*`` variables, then to the
    defaults (``E2B_DOMAIN=compute.anonalabs.com``).
    """
    if api_key is not None:
        os.environ["E2B_API_KEY"] = api_key
    if domain is not None:
        os.environ["E2B_DOMAIN"] = domain
    if api_url is not None:
        os.environ["E2B_API_URL"] = api_url
    if debug is not None:
        os.environ["E2B_DEBUG"] = "true" if debug else "false"
    apply_env()
