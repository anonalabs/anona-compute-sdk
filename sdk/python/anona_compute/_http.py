"""Small HTTP helper for the Anona-only gateway endpoints."""

from __future__ import annotations

from typing import Any, Dict, Optional

import httpx
from e2b.connection_config import ConnectionConfig
from e2b.exceptions import (
    AuthenticationException,
    InvalidArgumentException,
    NotFoundException,
    RateLimitException,
    SandboxException,
)


def _error(resp: httpx.Response) -> Exception:
    try:
        body = resp.json()
        message = body.get("message") or resp.text
    except ValueError:
        message = resp.text
    message = f"{resp.status_code}: {message}"
    if resp.status_code == 400:
        return InvalidArgumentException(message)
    if resp.status_code in (401, 403):
        return AuthenticationException(message)
    if resp.status_code == 404:
        return NotFoundException(message)
    if resp.status_code == 429:
        return RateLimitException(message)
    return SandboxException(message)


def _request(
    config: ConnectionConfig,
    method: str,
    path: str,
    body: Optional[Dict[str, Any]] = None,
) -> Optional[Dict[str, Any]]:
    headers = dict(config.headers)
    if config.api_key:
        headers["X-API-Key"] = config.api_key
    with httpx.Client(
        base_url=config.api_url,
        timeout=config.request_timeout,
        proxy=config.proxy,  # type: ignore[arg-type]
    ) as client:
        resp = client.request(method, path, json=body, headers=headers)
    if resp.status_code >= 400:
        raise _error(resp)
    if resp.status_code == 204 or not resp.content:
        return None
    return resp.json()


def post_json(
    config: ConnectionConfig,
    path: str,
    body: Optional[Dict[str, Any]] = None,
) -> Optional[Dict[str, Any]]:
    """POST to ``<api_url><path>`` with the sandbox's API key; return the JSON body."""
    return _request(config, "POST", path, body)


def put_json(
    config: ConnectionConfig,
    path: str,
    body: Optional[Dict[str, Any]] = None,
) -> Optional[Dict[str, Any]]:
    """PUT to ``<api_url><path>`` with the sandbox's API key; return the JSON body."""
    return _request(config, "PUT", path, body)


def get_json(config: ConnectionConfig, path: str) -> Optional[Dict[str, Any]]:
    """GET ``<api_url><path>`` with the sandbox's API key; return the JSON body."""
    return _request(config, "GET", path)
