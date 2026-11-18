"""Open a web page in a sandbox's browser, screenshot it, and print a link to watch it live.

Needs the desktop extra: pip install "anona-compute[desktop]"
"""
import time

from anona_compute.desktop import Sandbox

desk = Sandbox.create(timeout=600)  # the "desktop" template
try:
    desk.open("https://example.com")
    time.sleep(8)  # give the browser time to start and load the page
    png = desk.screenshot()
    with open("page.png", "wb") as f:
        f.write(png)
    print("saved page.png,", len(png), "bytes")
    print("watch the page live:", desk.live.url(mode="dom"))
finally:
    desk.kill()
