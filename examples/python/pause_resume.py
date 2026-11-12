"""Pause a sandbox (memory and disk are kept) and resume it later."""
from anona_compute import Sandbox

sbx = Sandbox.create(timeout=300)
try:
    sbx.commands.run("echo hello > /tmp/state.txt")
    sbx.pause()
    print("paused", sbx.sandbox_id)

    again = Sandbox.connect(sbx.sandbox_id)  # connecting resumes a paused sandbox
    print("after resume:", again.commands.run("cat /tmp/state.txt").stdout.strip())
finally:
    Sandbox.kill(sbx.sandbox_id)
