#!/bin/sh
# Install dependencies only when package-lock.json changed, then run Vite with HMR.
set -e
if ! cmp -s package-lock.json node_modules/.docker-lock 2>/dev/null; then
  npm ci --no-audit --no-fund
  cp package-lock.json node_modules/.docker-lock
fi
exec npm run dev -- --host 0.0.0.0 --port 5173 --strictPort
