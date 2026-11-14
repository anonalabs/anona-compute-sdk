"""``anona_compute.desktop.Sandbox``: the stock ``e2b_desktop.Sandbox`` plus Anona extras.

Every e2b-desktop call (mouse, keyboard, screenshot, ``stream.start`` and the
noVNC ``stream.get_url``) is inherited unchanged. The additions are the same
``live`` and ``ssh`` helpers as ``anona_compute.Sandbox`` and ``live_url()``, the
Anona live-view link for the desktop channel. That channel is served by the
template's guest broker (VNC on 127.0.0.1:5901, started on first use), so it
needs no ``stream.start()`` and never exposes port 6080.

Install with ``pip install "anona-compute[desktop]"``.
"""

from __future__ import annotations

from typing import Unpack

try:
    import e2b_desktop
except ImportError as e:  # pragma: no cover - exercised only without the extra
    raise ImportError('anona_compute.desktop needs e2b-desktop: pip install "anona-compute[desktop]"') from e

from e2b.connection_config import ApiParams

from anona_compute._wait import CapacityWaitMixin
from anona_compute.idle import IdlePause
from anona_compute.live import MAX_TTL, Live
from anona_compute.sandbox import anona_defaults
from anona_compute.ssh import Ssh


class Sandbox(e2b_desktop.Sandbox, CapacityWaitMixin):
    """``e2b_desktop.Sandbox`` with ``live``, ``ssh``, ``idle_pause`` and ``live_url()``.

    ``create`` and ``connect`` take ``wait_for_capacity`` and
    ``on_capacity_wait`` as ``anona_compute.Sandbox`` does; the wait repeats
    only the API call, never the desktop's start-up.
    """

    @classmethod
    def _resolve_api_params(cls, **opts: Unpack[ApiParams]) -> ApiParams:
        merged = dict(anona_defaults())
        resolved = super()._resolve_api_params(**opts)
        merged.update({k: v for k, v in resolved.items() if v is not None})
        return merged  # type: ignore[return-value]

    @property
    def live(self) -> Live:
        return Live(self.sandbox_id, self.connection_config)

    @property
    def ssh(self) -> Ssh:
        return Ssh(self.sandbox_id, self.connection_config)

    @property
    def idle_pause(self) -> IdlePause:
        """Idle pause: ``get()``, ``set(minutes)``, ``off()``, as on ``anona_compute.Sandbox``."""
        return IdlePause(self.sandbox_id, self.connection_config)

    def live_url(self, control: bool = True, ttl: int = MAX_TTL) -> str:
        """Embed URL for the desktop channel (control scope by default: the
        desktop channel carries RFB input, which a view token may not send)."""
        return self.live.url(mode="desktop", scope="control" if control else "view", ttl=ttl)
