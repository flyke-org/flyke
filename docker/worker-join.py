"""Get a fresh join token from the control plane, then run an ephemeral worker.

Each (re)start registers a new worker and deregisters it on exit, so no
credential is kept between restarts and a reset server never leaves a stale one.
"""

import json
import os
import sys
import time
import urllib.request

url = os.environ.get("FLYKE_URL", "http://server:8742").rstrip("/")
pool = os.environ.get("FLYKE_POOL", "local")
admin = os.environ.get("FLYKE_ADMIN_TOKEN", "")


def join_token() -> str:
    req = urllib.request.Request(
        f"{url}/api/v1/pools/{pool}/join-tokens",
        data=json.dumps({"ttlSeconds": 600, "maxUses": 1}).encode(),
        headers={
            "Content-Type": "application/json",
            **({"Authorization": f"Bearer {admin}"} if admin else {}),
        },
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=5) as resp:
        return json.load(resp)["token"]


for _attempt in range(60):
    try:
        token = join_token()
        break
    except Exception as e:  # server still (re)building
        print(f"waiting for control plane at {url}: {e}", file=sys.stderr, flush=True)
        time.sleep(2)
else:
    sys.exit("could not get a join token")

os.execvp(
    "flyke-worker",
    [
        "flyke-worker",
        "--state-dir",
        "/tmp/flyke-worker",
        "join",
        "--url",
        url,
        "--token",
        token,
        "--ephemeral",
    ],
)
