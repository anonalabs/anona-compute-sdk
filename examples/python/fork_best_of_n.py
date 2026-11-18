"""Set a sandbox up once, fork it, try a different approach in each copy, keep the best.

Each fork starts from the parent's exact state: files, memory and running processes.
"""
from anona_compute import Sandbox

# Stand-ins for what your agent would try: three ways to do the same job.
attempts = ["sort -n", "sort -rn", "sort -un"]

with Sandbox.create("base", timeout=600) as sbx:
    # The expensive setup happens once; every fork inherits it.
    sbx.files.write("/home/user/data.txt", "3\n1\n2\n3\n")

    # One fork request for all copies: each request keeps a checkpoint, capped per team.
    forks = sbx.fork(count=len(attempts), timeout=300)
    results = {}
    for attempt, f in zip(attempts, forks):
        if isinstance(f, Exception):  # a copy that did not fit, e.g. the plan's limit
            print(f"{attempt!r}: not forked: {f}")
            continue
        try:
            results[attempt] = f.commands.run(f"{attempt} /home/user/data.txt").stdout.split()
            print(f"{attempt!r} in {f.sandbox_id}: {results[attempt]}")
        finally:
            f.kill()

best = min(results, key=lambda a: len(results[a]))  # your own scoring goes here
print("kept:", best, results[best])
