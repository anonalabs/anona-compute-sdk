"""Test-only fake of the gateway endpoints the SDK extras call.

It is an in-process HTTP server (like Go's httptest.Server). It speaks the wire
contract from plan 05 sections 3.4 and 3.7 and records every request. It is
never used outside tests.
"""

from __future__ import annotations

import base64
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, List, Optional, Tuple

API_KEY = "e2b_" + "0123456789abcdef" * 2 + "01234567"


def _b64(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def fake_jws(claims: Dict[str, Any]) -> str:
    header = {"alg": "EdDSA", "kid": "test", "typ": "anona-view+jwt"}
    return ".".join(
        [_b64(json.dumps(header).encode()), _b64(json.dumps(claims).encode()), _b64(b"sig")]
    )


class FakeGateway:
    def __init__(self) -> None:
        self.requests: List[Tuple[str, str, Dict[str, str], Optional[Any]]] = []
        self.revoked: List[str] = []
        self.sandboxes: Dict[str, Dict[str, Any]] = {}
        # Canned errors: (path, status, body, headers), answered in order to
        # POSTs to that path before the normal handling resumes.
        self.faults: List[Tuple[str, int, Dict[str, Any], Dict[str, str]]] = []
        gw = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args: Any) -> None:  # keep test output clean
                pass

            def _send(self, code: int, body: Any = None, headers: Optional[Dict[str, str]] = None) -> None:
                data = b"" if body is None else json.dumps(body).encode()
                self.send_response(code)
                if body is not None:
                    self.send_header("Content-Type", "application/json")
                for k, v in (headers or {}).items():
                    self.send_header(k, v)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_POST(self) -> None:  # noqa: N802
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b""
                body = json.loads(raw) if raw else None
                gw.requests.append(
                    ("POST", self.path, {k.lower(): v for k, v in self.headers.items()}, body)
                )
                if self.headers.get("X-API-Key") != API_KEY:
                    return self._send(401, {"code": 401, "message": "Invalid API key"})
                parts = self.path.strip("/").split("/")
                for i, (path, status, fbody, fheaders) in enumerate(gw.faults):
                    if path == self.path:
                        del gw.faults[i]
                        return self._send(status, fbody, fheaders)
                if self.path == "/v2/sandboxes":
                    sid = f"sbx{len(gw.sandboxes) + 1:04d}"
                    gw.sandboxes[sid] = {"templateID": body.get("templateID", "base")}
                    return self._send(201, gw.sandbox_json(sid))
                if parts[:2] == ["v2", "sandboxes"] and parts[-1] == "connect":
                    sid = parts[2]
                    if sid not in gw.sandboxes:
                        return self._send(404, {"code": 404, "message": "sandbox not found"})
                    return self._send(201, gw.sandbox_json(sid))
                if parts[:2] == ["v1", "sandboxes"] and len(parts) == 4:
                    sid, action = parts[2], parts[3]
                    if sid not in gw.sandboxes:
                        return self._send(404, {"code": 404, "message": "sandbox not found"})
                    if action == "viewer-tokens":
                        now = int(time.time())
                        ttl = int(body["ttlSeconds"])
                        claims = {
                            "iss": "https://api.compute.anonalabs.com",
                            "aud": "stream.compute.anonalabs.com",
                            "sub": "key_test",
                            "tid": "org_test",
                            "sbx": sid,
                            "ch": body["channels"],
                            "scp": body["scope"],
                            "ep": 1,
                            "iat": now,
                            "nbf": now,
                            "exp": now + ttl,
                            "jti": "vt_test",
                        }
                        token = fake_jws(claims)
                        mode = body["channels"][0]
                        # The real gateway answers 200 (stream/token/handlers.go).
                        return self._send(
                            200,
                            {
                                "token": token,
                                "expiresAt": time.strftime(
                                    "%Y-%m-%dT%H:%M:%SZ", time.gmtime(now + ttl)
                                ),
                                "embedUrl": f"https://stream.compute.anonalabs.com/embed/v1/{sid}?mode={mode}#t={token}",
                            },
                        )
                    if action == "viewer-tokens:revoke-all":
                        gw.revoked.append(sid)
                        return self._send(200, {"epoch": len(gw.revoked)})
                    if action == "ssh-certs":
                        key = body["publicKey"]
                        # 200, and the proxy command names the sandbox (sshca/handler.go).
                        return self._send(
                            200,
                            {
                                "certificate": "ssh-ed25519-cert-v01@openssh.com AAAAfake "
                                + key.split()[1][:8],
                                "user": "user",
                                "host": sid,
                                "proxyCommand": f"anona-compute ssh --proxy {sid}",
                                "expiresAt": time.strftime(
                                    "%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + 600)
                                ),
                            },
                        )
                return self._send(404, {"code": 404, "message": "not found"})

        self._server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self._server.server_address[1]}"
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)

    def fail(
        self,
        path: str,
        status: int,
        body: Dict[str, Any],
        headers: Optional[Dict[str, str]] = None,
        times: int = 1,
    ) -> None:
        """Answer the next ``times`` POSTs to ``path`` with this error."""
        self.faults.extend([(path, status, body, dict(headers or {}))] * times)

    def posts(self, path: str) -> int:
        return sum(1 for m, p, _, _ in self.requests if m == "POST" and p == path)

    def sandbox_json(self, sid: str) -> Dict[str, Any]:
        return {
            "sandboxID": sid,
            "templateID": self.sandboxes[sid]["templateID"],
            "clientID": "client",
            "envdVersion": "0.2.0",
            "envdAccessToken": "envd-token",
            "domain": "sbx.compute.anonalabs.com",
        }

    def __enter__(self) -> "FakeGateway":
        self._thread.start()
        return self

    def __exit__(self, *exc: Any) -> None:
        self._server.shutdown()
        self._server.server_close()
