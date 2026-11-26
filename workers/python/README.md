# flyke-worker (Python)

The Flyke data plane for Python workflows. It speaks the `flyke.worker.v1`
protocol (Connect, HTTP/1.1 + JSON) and uses the `flyke` SDK to replay
workflow steps and execute tasks. It never accepts inbound connections.

```bash
pip install flyke-worker
flyke-worker join --url https://flyke.example.com --token flyke_jt_abc.xyz --labels region=eu
```

Or with Docker:

```bash
docker run -d --restart=always -v flyke-worker:/var/lib/flyke-worker \
  ghcr.io/flyke-org/worker-python join --url https://flyke.example.com --token flyke_jt_abc.xyz
```

The credential is saved (mode 0600) in `~/.flyke-worker/credentials.json`
(`--state-dir` / `FLYKE_WORKER_HOME`). `flyke-worker start` restarts with it,
`flyke-worker leave` deregisters. SIGTERM drains: running work finishes, no new
work is accepted.
