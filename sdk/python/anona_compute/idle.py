"""Idle pause: pause a sandbox once nothing has used it for a while.

Gateway contract (Anona routes, not E2B):

- ``PUT /v1/sandboxes/{id}/auto-pause`` with ``{"idleMinutes": n}`` returns the
  sandbox's access settings. ``n`` is 1 to 10080 (one week); ``0`` turns idle
  pausing off.
- ``GET /v1/sandboxes/{id}/access`` returns ``idlePauseMinutes`` among them.

This is **not** the same as E2B's ``autoPause``/``on_timeout``, which decides what
happens when the sandbox's *deadline* runs out. Idle pause has no deadline: the
sandbox keeps running for as long as it is used, and pauses once it is not.

What counts as use, so the clock restarts: any
request to any of the sandbox's ports except envd's ``GET /health``; an open live
view, terminal, SSH session or byte pipe; and any process envd started that is
still running. Not counted: CPU burned inside the guest by something envd did not
start, outbound traffic the sandbox makes itself, and API calls *about* the
sandbox. A sandbox busy on work it started by itself will still pause.

Resume with ``Sandbox.connect(sandbox_id)``.
"""

from __future__ import annotations

from urllib.parse import quote
from typing import TYPE_CHECKING

from e2b.exceptions import InvalidArgumentException

from anona_compute._http import get_json, put_json

if TYPE_CHECKING:
    from e2b.connection_config import ConnectionConfig

#: Longest idle time the gateway accepts (store.MaxIdlePauseMinutes): one week.
MAX_IDLE_PAUSE_MINUTES = 10080


class IdlePause:
    """``sandbox.idle_pause``: read and set this sandbox's idle-pause time."""

    def __init__(self, sandbox_id: str, config: "ConnectionConfig"):
        self._sandbox_id = sandbox_id
        self._config = config

    @property
    def _path(self) -> str:
        return f"/v1/sandboxes/{quote(self._sandbox_id, safe='')}"

    def get(self) -> int:
        """Minutes of idleness before this sandbox pauses; ``0`` when off."""
        body = get_json(self._config, f"{self._path}/access") or {}
        return int(body.get("idlePauseMinutes") or 0)

    def set(self, minutes: int) -> int:
        """Pause this sandbox after ``minutes`` without use. ``0`` turns it off.

        Returns the value the gateway recorded, which is what later reads will show.
        """
        if not isinstance(minutes, int) or isinstance(minutes, bool):
            raise InvalidArgumentException("minutes must be an int")
        if minutes < 0 or minutes > MAX_IDLE_PAUSE_MINUTES:
            raise InvalidArgumentException(
                f"minutes must be 0 (off) to {MAX_IDLE_PAUSE_MINUTES}"
            )
        body = put_json(self._config, f"{self._path}/auto-pause", {"idleMinutes": minutes}) or {}
        return int(body.get("idlePauseMinutes") or 0)

    def off(self) -> int:
        """Stop pausing this sandbox when it is idle. Same as ``set(0)``."""
        return self.set(0)
