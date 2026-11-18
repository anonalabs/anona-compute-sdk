"""One sandbox per end user of your app: find it again by metadata, pause it when idle.

Run this on your server; never send the API key to a browser.
"""
from anona_compute import Sandbox
from e2b import SandboxQuery, SandboxState


def sandbox_for(user_id: str) -> Sandbox:
    # Ask for paused sandboxes too: a returning user's sandbox is usually paused.
    query = SandboxQuery(metadata={"app_user": user_id}, state=[SandboxState.RUNNING, SandboxState.PAUSED])
    found = Sandbox.list(query=query).next_items()
    if found:
        return Sandbox.connect(found[0].sandbox_id)  # resumes it if paused
    sbx = Sandbox.create("base", timeout=3600, metadata={"app_user": user_id})
    sbx.idle_pause.set(15)  # pause after 15 minutes nobody uses it
    return sbx


sbx = sandbox_for("user_42")
sbx.files.write("/home/user/notes.txt", "kept across visits\n")
sbx.pause()  # what idle pause does on its own

again = sandbox_for("user_42")  # the user comes back
print("same sandbox:", again.sandbox_id == sbx.sandbox_id)
print("their file:", again.files.read("/home/user/notes.txt").strip())

# A short-lived link for your page's iframe: a terminal the user can type into.
print("embed:", again.live.url(mode="pty", scope="control", ttl=300).split("#")[0])
again.kill()
