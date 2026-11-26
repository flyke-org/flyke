#!/bin/sh
# Restart the worker whenever the SDK or worker source changes.
exec watchfiles --filter python --sigint-timeout 10 \
  "python /src/docker/worker-join.py" \
  /src/sdk/src /src/workers/python/src
