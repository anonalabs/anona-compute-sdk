"""Create a sandbox with the stock e2b SDK and run a command in it."""
from e2b import Sandbox  # pip install e2b; reads E2B_DOMAIN and E2B_API_KEY

sbx = Sandbox.create(timeout=300)
try:
    result = sbx.commands.run("uname -sr && nproc")
    print(result.stdout)
finally:
    sbx.kill()
