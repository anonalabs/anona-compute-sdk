"""Upload a file, run analysis code (for example code a model wrote) against it, read the result back.

The python template ships pandas and numpy in /opt/venv, which is on the PATH.
"""
from textwrap import dedent

from anona_compute import Sandbox

data = "region,sales\nnorth,120\nsouth,80\nnorth,30\n"  # the user's upload

analysis = dedent("""
    import pandas as pd
    df = pd.read_csv("/home/user/sales.csv")
    df.groupby("region")["sales"].sum().to_csv("/home/user/by_region.csv")
""")

with Sandbox.create("python", timeout=300) as sbx:
    sbx.files.write("/home/user/sales.csv", data)
    sbx.files.write("/home/user/analysis.py", analysis)
    sbx.commands.run("python /home/user/analysis.py")  # raises with stderr if the code fails
    print(sbx.files.read("/home/user/by_region.csv"))
