"""Get a live-view link to a sandbox's terminal."""
from anona_compute import Sandbox

sbx = Sandbox.create(timeout=300)
try:
    url = sbx.live.url(mode="pty", scope="control", ttl=300)  # a terminal you can type into
    print("open this in a browser within 5 minutes:", url)
    input("press Enter to delete the sandbox ")
finally:
    sbx.kill()
