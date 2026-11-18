"""Let code in a sandbox use a credential it can never read, and reach only one host.

Before running, store a secret named demo_token that may be sent to httpbin.org:

    printf 'not-a-real-token' | anona-compute vault create demo_token --host httpbin.org --value-file -

httpbin.org/headers echoes back the request's headers, so you can see the proxy add it.

Creating a sandbox with a new vault rule needs an API key with the vault:bind scope
(pick it in a custom key; no preset has it). Without it the create is refused with 403.
"""
from anona_compute import Sandbox

sbx = Sandbox.create(
    "base",
    timeout=300,
    network={
        "deny_out": ["0.0.0.0/0"],     # no public internet ...
        "allow_out": ["httpbin.org"],  # ... except this host
        "rules": {"httpbin.org": [{"transform": {"headers": {"Authorization": "Bearer ${vault.demo_token}"}}}]},
    },
)
try:
    env = sbx.commands.run("env").stdout
    print("token in the sandbox environment:", "not-a-real-token" in env)

    echoed = sbx.commands.run("curl -sS https://httpbin.org/headers").stdout
    print("httpbin saw:", [l.strip() for l in echoed.splitlines() if "Authorization" in l])

    blocked = sbx.commands.run("curl -sS -m 5 -o /dev/null -w '%{http_code}' https://example.com || true").stdout
    print("example.com:", blocked or "no connection")
finally:
    sbx.kill()
