"""SSH helpers: short-lived user certificates and an ``ssh_config`` snippet.

Gateway contract (plan 05, section 3.7):

- ``POST /v1/sandboxes/{id}/ssh-certs`` with ``{"publicKey": "<openssh public key>"}``
  returns ``{"certificate", "user", "host", "proxyCommand", "expiresAt"}``.
  The certificate is valid for 10 minutes on that sandbox only (principal
  ``sbx:<id>``); it logs in as ``user``.

Transport is OpenSSH ``ProxyCommand anona-compute ssh --proxy %h``, so ``scp``, ``rsync``
and editors with Remote-SSH work through a stock ``ssh`` client.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import TYPE_CHECKING, Optional, Union
from urllib.parse import quote

from e2b.exceptions import InvalidArgumentException

from anona_compute._http import post_json

if TYPE_CHECKING:
    from e2b.connection_config import ConnectionConfig

DEFAULT_USER = "user"
DEFAULT_PROXY_COMMAND = "anona-compute ssh --proxy %h"
# The key types the gateway signs.
_KEY_TYPES = (
    "ssh-ed25519",
    "ecdsa-sha2-nistp256",
    "ecdsa-sha2-nistp384",
    "ecdsa-sha2-nistp521",
    "ssh-rsa",
)


def _is_key_line(text: str) -> bool:
    # The first field must BE a key type, not merely start with one:
    # "ssh-ed25519-cert-v01@openssh.com ..." is a certificate, which the
    # gateway refuses.
    return text.split(" ", 1)[0] in _KEY_TYPES


@dataclass(frozen=True)
class SshCertificate:
    """A signed OpenSSH user certificate for one sandbox."""

    cert: str = field(repr=False)
    user: str
    host: str
    proxy_command: str
    expires_at: Optional[datetime]

    def write(self, path: Union[str, os.PathLike]) -> Path:
        """Write the certificate (mode 0600), for example next to the key as ``<key>-cert.pub``."""
        p = Path(path).expanduser()
        p.parent.mkdir(parents=True, exist_ok=True)
        fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w") as f:
            f.write(self.cert.rstrip("\n") + "\n")
        return p


def _read_public_key(public_key: Union[str, os.PathLike]) -> str:
    text = str(public_key).strip()
    if not _is_key_line(text):
        path = Path(text).expanduser()
        if not path.is_file():
            raise InvalidArgumentException(
                "public_key must be an OpenSSH public key or the path to a .pub file"
            )
        text = path.read_text().strip()
    if not _is_key_line(text):
        raise InvalidArgumentException(f"unsupported public key type; expected one of {_KEY_TYPES}")
    if "PRIVATE KEY" in text:
        raise InvalidArgumentException("refusing to send a private key")
    return text


class Ssh:
    """``sandbox.ssh``: certificates and client config for one sandbox."""

    def __init__(self, sandbox_id: str, config: "ConnectionConfig"):
        self._sandbox_id = sandbox_id
        self._config = config

    def certificate(self, public_key: Union[str, os.PathLike]) -> SshCertificate:
        """Ask the gateway to sign ``public_key`` (a key string or a ``.pub`` path)."""
        body = post_json(
            self._config,
            f"/v1/sandboxes/{quote(self._sandbox_id, safe='')}/ssh-certs",
            {"publicKey": _read_public_key(public_key)},
        )
        if not body:
            raise InvalidArgumentException("gateway returned an empty certificate response")
        expires = body.get("expiresAt")
        return SshCertificate(
            cert=body["certificate"],
            user=body.get("user") or DEFAULT_USER,
            host=body.get("host") or self._sandbox_id,
            proxy_command=body.get("proxyCommand") or DEFAULT_PROXY_COMMAND,
            expires_at=datetime.fromisoformat(expires.replace("Z", "+00:00")) if expires else None,
        )

    def config(
        self,
        identity_file: Union[str, os.PathLike] = "~/.ssh/anona_ed25519",
        certificate_file: Optional[Union[str, os.PathLike]] = None,
        alias: Optional[str] = None,
        user: str = DEFAULT_USER,
        proxy_command: str = DEFAULT_PROXY_COMMAND,
    ) -> str:
        """Return an ``ssh_config`` block for this sandbox. Makes no network call."""
        cert = certificate_file or f"{identity_file}-cert.pub"
        name = alias or f"anona-{self._sandbox_id}"
        return "\n".join(
            [
                f"Host {name}",
                f"  HostName {self._sandbox_id}",
                f"  User {user}",
                f"  ProxyCommand {proxy_command}",
                f"  IdentityFile {identity_file}",
                f"  CertificateFile {cert}",
                "  IdentitiesOnly yes",
                "  StrictHostKeyChecking accept-new",
                "",
            ]
        )
