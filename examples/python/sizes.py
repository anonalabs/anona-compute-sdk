"""Create a sandbox at a chosen size: small, medium, large or xl."""
from anona_compute import Sandbox  # pip install anona-compute

sbx = Sandbox.create("python", size="large", timeout=300)  # 4 vCPU, 8 GiB, 40 GB disk
try:
    print(sbx.commands.run("nproc && free -g | sed -n 2p && df -h / | tail -1").stdout)
finally:
    sbx.kill()
