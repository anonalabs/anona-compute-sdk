"""Serve a port inside a sandbox and open it through a signed preview link."""
import os
import time

import httpx
from anona_compute import Sandbox

sbx = Sandbox.create("python", timeout=300)
try:
    sbx.commands.run("python3 -m http.server 3000 --bind 127.0.0.1", background=True)
    time.sleep(2)

    api = httpx.Client(base_url="https://api.compute.anonalabs.com", headers={"X-API-Key": os.environ["ANONA_API_KEY"]})
    link = api.post(f"/v1/sandboxes/{sbx.sandbox_id}/preview-links", json={"port": 3000, "ttlSeconds": 900}).json()
    print("preview link:", link["url"].split("?")[0], "expires", link["expiresAt"])

    # The first response sets a cookie and redirects; keep cookies across it.
    with httpx.Client(follow_redirects=True) as browser:
        page = browser.get(link["url"])
        print("status", page.status_code)
finally:
    sbx.kill()
