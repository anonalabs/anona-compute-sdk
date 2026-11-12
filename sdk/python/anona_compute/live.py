"""Live view helpers: mint viewer tokens and embed URLs, revoke viewers.

Gateway contract (plan 05, section 3.4):

- ``POST /v1/sandboxes/{id}/viewer-tokens`` with
  ``{"scope": "view"|"control", "channels": [...], "ttlSeconds": n}`` returns
  ``{"token", "expiresAt", "embedUrl"}``.
- ``POST /v1/sandboxes/{id}/viewer-tokens:revoke-all`` returns 200 ``{"epoch": n}``.
"""

from __future__ import annotations

import base64
import json
from dataclasses import dataclass, field
from datetime import datetime
from typing import TYPE_CHECKING, Any, Dict, Iterable, List, Optional
from urllib.parse import quote

from e2b.exceptions import InvalidArgumentException

from anona_compute._http import post_json

if TYPE_CHECKING:
    from e2b.connection_config import ConnectionConfig

SCOPES = ("view", "control")
CHANNELS = ("dom", "pty", "desktop", "thumb")
MIN_TTL = 30
MAX_TTL = 600


@dataclass(frozen=True)
class ViewerToken:
    """A signed viewer token. ``repr`` never prints the token itself."""

    token: str = field(repr=False)
    expires_at: datetime
    embed_url: str = field(repr=False)

    def claims(self) -> Dict[str, Any]:
        """Decode the JWS payload without checking the signature (for inspection only)."""
        parts = self.token.split(".")
        if len(parts) != 3:
            raise ValueError("viewer token is not a compact JWS")
        payload = parts[1] + "=" * (-len(parts[1]) % 4)
        return json.loads(base64.urlsafe_b64decode(payload))


def _parse_time(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def _validate(scope: str, channels: List[str], ttl: int) -> None:
    if scope not in SCOPES:
        raise InvalidArgumentException(f"scope must be one of {SCOPES}, got {scope!r}")
    if not channels:
        raise InvalidArgumentException("channels must not be empty")
    unknown = [c for c in channels if c not in CHANNELS]
    if unknown:
        raise InvalidArgumentException(f"unknown channels {unknown}; allowed {CHANNELS}")
    # The gateway decodes ttlSeconds into an int and answers 400 to 60.5, so a
    # fraction is refused here, as the TypeScript SDK does (Number.isInteger).
    if isinstance(ttl, bool) or not isinstance(ttl, int) or not MIN_TTL <= ttl <= MAX_TTL:
        raise InvalidArgumentException(f"ttl must be a whole number of seconds from {MIN_TTL} to {MAX_TTL}")


class Live:
    """``sandbox.live``: viewer tokens for the live view of one sandbox."""

    def __init__(self, sandbox_id: str, config: "ConnectionConfig"):
        self._sandbox_id = sandbox_id
        self._config = config

    def _path(self, suffix: str = "") -> str:
        return f"/v1/sandboxes/{quote(self._sandbox_id, safe='')}/viewer-tokens{suffix}"

    def token(
        self,
        scope: str = "view",
        channels: Optional[Iterable[str]] = None,
        ttl: int = MAX_TTL,
    ) -> ViewerToken:
        """Mint a viewer token (default: view-only DOM channel, 600 s)."""
        chans = list(channels) if channels is not None else ["dom"]
        _validate(scope, chans, ttl)
        body = post_json(
            self._config,
            self._path(),
            {"scope": scope, "channels": chans, "ttlSeconds": ttl},
        )
        if not body:
            raise InvalidArgumentException("gateway returned an empty viewer token response")
        return ViewerToken(
            token=body["token"],
            expires_at=_parse_time(body["expiresAt"]),
            embed_url=body["embedUrl"],
        )

    def url(self, mode: str = "dom", scope: str = "view", ttl: int = MAX_TTL) -> str:
        """Mint a token for one channel and return its embed URL."""
        return self.token(scope=scope, channels=[mode], ttl=ttl).embed_url

    def revoke_all(self) -> None:
        """Close every open viewer of this sandbox and invalidate issued tokens."""
        post_json(self._config, self._path(":revoke-all"))
