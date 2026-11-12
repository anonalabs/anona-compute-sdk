"""``anona_compute.Sandbox``: the stock ``e2b.Sandbox`` plus Anona defaults and extras."""

from __future__ import annotations

import os
from typing import Any, Literal, Unpack

import e2b
from e2b.connection_config import ApiParams

from anona_compute._config import DEFAULT_DOMAIN, compute_key
from anona_compute.idle import IdlePause
from anona_compute.live import Live
from anona_compute.ssh import Ssh


SandboxSize = Literal["small", "medium", "large", "xl"]
SIZES: tuple[str, ...] = ("small", "medium", "large", "xl")
_SIZE_ALIASES = {"s": "small", "m": "medium", "l": "large", "xlarge": "xl", "x-large": "xl"}

# The E2B SDK cannot send the create body's ``size`` (an Anona extension), so
# the size rides in this metadata key; the gateway consumes it and never
# stores it as metadata.
SIZE_METADATA_KEY = "anona.size"


def size_metadata(size: str, metadata: dict[str, str] | None = None) -> dict[str, str]:
    """Return ``metadata`` with the sandbox size set (small, medium, large or xl)."""
    name = _SIZE_ALIASES.get(size.strip().lower(), size.strip().lower())
    if name not in SIZES:
        raise ValueError(f"unknown sandbox size {size!r}: use one of {', '.join(SIZES)}")
    out = dict(metadata or {})
    out[SIZE_METADATA_KEY] = name
    return out


def anona_defaults() -> ApiParams:
    """API params that point the SDK at Anona unless E2B_* variables say otherwise."""
    params: ApiParams = {}
    if not os.getenv("E2B_DOMAIN"):
        params["domain"] = os.getenv("ANONA_DOMAIN") or DEFAULT_DOMAIN
    key = compute_key(os.getenv("ANONA_API_KEY"))
    if not os.getenv("E2B_API_KEY") and key:
        params["api_key"] = key
    if not os.getenv("E2B_API_URL") and os.getenv("ANONA_API_URL"):
        params["api_url"] = os.environ["ANONA_API_URL"]
    return params


class Sandbox(e2b.Sandbox):
    """Every ``e2b.Sandbox`` method works unchanged; ``live``, ``ssh`` and ``idle_pause`` are added."""

    @classmethod
    def _resolve_api_params(cls, **opts: Unpack[ApiParams]) -> ApiParams:
        merged = dict(anona_defaults())
        resolved = super()._resolve_api_params(**opts)
        merged.update({k: v for k, v in resolved.items() if v is not None})
        return merged  # type: ignore[return-value]

    @classmethod
    def create(cls, template: str | None = None, *args: Any, size: str | None = None, **kwargs: Any):  # type: ignore[override]
        """``e2b.Sandbox.create`` plus ``size``: small, medium, large or xl.

        A size decides the sandbox's vCPU, memory and disk; without one the
        sandbox runs at its template's own size. A size below the template's
        minimum, or not in your plan, is refused with the sizes that would work.
        """
        if size is not None:
            if len(args) >= 2:  # timeout, metadata given positionally
                lst = list(args)
                lst[1] = size_metadata(size, lst[1])
                args = tuple(lst)
            else:
                kwargs["metadata"] = size_metadata(size, kwargs.get("metadata"))
        return super().create(template, *args, **kwargs)

    @property
    def live(self) -> Live:
        """Live view: ``token()``, ``url()``, ``revoke_all()``."""
        return Live(self.sandbox_id, self.connection_config)

    @property
    def ssh(self) -> Ssh:
        """SSH: ``certificate(public_key)`` and ``config()``."""
        return Ssh(self.sandbox_id, self.connection_config)

    @property
    def idle_pause(self) -> IdlePause:
        """Idle pause: ``get()``, ``set(minutes)``, ``off()``.

        Pauses the sandbox once nothing has used it for that long. Different from
        E2B's ``on_timeout``, which acts when the deadline runs out.
        """
        return IdlePause(self.sandbox_id, self.connection_config)
