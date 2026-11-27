# Developing Flyke locally

There are two ways to work on Flyke. Pick one:

| | [Without Docker](#option-a-without-docker) | [With Docker Compose](#option-b-docker-compose) |
|---|---|---|
| Needs | Go, Python, uv, Node.js | Docker only (plus the CLI on the host to run workflows) |
| Start | `make dev build`, then `./bin/flyke server --dev` | `docker compose up --build` |
| Reload on change | rebuild (`make build`) or `go run`; UI via `make ui-dev` | automatic for the server, worker and UI |
| Best for | fast edit-test cycles, debugging with a debugger | trying the real multi-process shape, working on the UI |

Both run the same pieces, the control plane, one Python worker and the dashboard, and they
speak the same protocol as production.

---

## Prerequisites

| Tool | Version | Used for |
|---|---|---|
| Go | 1.26+ | control plane and CLI |
| Python | 3.11+ | SDK, worker, your workflows |
| [uv](https://docs.astral.sh/uv/) | recent | Python workspace (`sdk/` + `workers/python/`) |
| Node.js + npm | LTS | dashboard |
| [buf](https://buf.build/docs/installation) + `protoc-gen-go` + `protoc-gen-connect-go` | recent | only if you change `api/proto` |
| Docker | recent | Option B |

---

## Option A: without Docker

### 1. Install and build

```bash
git clone https://github.com/flyke-org/flyke.git && cd flyke
make dev      # uv sync --python 3.12: ./.venv with `flyke` and `flyke-worker` (editable) + pre-commit hooks
make build    # npm ci + vite build, then go build → ./bin/flyke (with the dashboard) and ./bin/flyke-server
```

Because the Python packages are installed in editable mode, changes under `sdk/` and
`workers/python/` apply the next time a worker starts. Go changes need `make build` again.

### 2. Run a workflow, the shortest loop

```bash
./bin/flyke run examples/hello.py:Hello --name=Ada -f
```

With no server configured, this starts an **ephemeral** control plane in the CLI process
and a worker subprocess, runs the workflow, prints its logs and stops both. The worker
reads your code in place, so edit `examples/hello.py` and run again.

The CLI finds the Python interpreter by checking `$FLYKE_PYTHON`, then the active
virtualenv, then a `.venv` next to the workflow file or in a parent folder. In this
repository that is `./.venv`, created by `make dev`.

### 3. Run a long-lived dev server

```bash
./bin/flyke server --dev                     # control plane + 1 local worker + dashboard on http://127.0.0.1:8742
./bin/flyke server --dev --data-dir ./.dev   # same, but keeps its state across restarts
./bin/flyke server --dev --worker=false      # no local worker: start your own (see below)
```

Every other `flyke` command (`run`, `emit`, `logs`, `runs ls`…) finds this server through
`~/.flyke/local.json` and uses it instead of starting its own.

### 4. Work on the dashboard

```bash
./bin/flyke server --dev    # terminal 1: the API on :8742
make ui-dev                 # terminal 2: Vite with hot reload on http://localhost:5173
```

Vite proxies API calls to `$FLYKE_SERVER` (default `http://127.0.0.1:8742`).

### 5. Work on the control plane

Run it straight from source while you iterate:

```bash
cd flyke && go run ./cmd/flyke-server -dev -data-dir /tmp/flyke-dev
```

Then point the CLI at it and start a worker by hand (next step). For a debugger, run the
same package under `dlv debug ./cmd/flyke-server -- -dev`.

### 6. Work on the worker

Start a worker against any control plane:

```bash
./bin/flyke --server http://127.0.0.1:8742 pools token local --ttl 10m   # prints a join token and the join command
.venv/bin/flyke-worker --state-dir /tmp/fw join \
  --url http://127.0.0.1:8742 --token flyke_jt_… --ephemeral --slots 1
```

`--ephemeral` deregisters the worker when it exits, so restarting it leaves no stale worker
behind. `--slots 1` makes scheduling easy to follow. A separate `--state-dir` keeps this
worker's credential apart from any other worker on the same machine.

### 7. Run the tests

```bash
make test        # go vet + go test -race (api, flyke, cli), pytest, UI typecheck
make test-go     # Go only
make test-py     # Python only: uv run pytest -q
make fmt         # ruff lint + format (the pre-commit hook does this on each commit)
```

---

## Option B: Docker Compose

```bash
docker compose up --build
```

| Service | URL | What it is | Reloads when |
|---|---|---|---|
| `server` | http://localhost:8742 | control plane (`flyke-server -dev`), data in the `server-data` volume | `flyke/` or `api/` changes (rebuilt by [air](https://github.com/air-verse/air)) |
| `ui` | http://localhost:5173 | dashboard served by Vite | `ui/` changes (hot module reload) |
| `worker` | — | one Python worker, pool `local`, 4 slots | `sdk/` or `workers/python/` changes (restarted by `watchfiles`) |

The source tree is bind-mounted into the containers, so you don't rebuild images after code
changes. Rebuild only when dependencies change (`docker compose up -d --build worker`).

Each time the worker (re)starts, it asks the server for a fresh single-use join token and
registers as an **ephemeral** worker (`docker/worker-join.py`), so a server reset never
leaves it with a stale credential.

### Run workflows against the stack

From the host, with the CLI built (`make build`) or installed:

```bash
export FLYKE_SERVER=http://127.0.0.1:8742
flyke run examples/hello.py:Hello --bundle -f
flyke runs ls
```

Use `--bundle`. Without it, a loopback server is told to read your code from a path on the
host, which the worker container cannot see.

### With authentication

```bash
FLYKE_ADMIN_TOKEN=$(openssl rand -hex 32) docker compose up --build
export FLYKE_TOKEN=<the same token>    # for the CLI on the host
```

The server and the worker share the token from the environment.

### Useful commands

```bash
docker compose logs -f server worker     # follow logs
docker compose restart worker            # restart only the worker
docker compose down                      # stop, keep data
docker compose down -v                   # stop and wipe the server's data and caches
```

---

## Environment variables

| Variable | Used by | Meaning |
|---|---|---|
| `FLYKE_SERVER` | CLI | control plane URL; when unset, the CLI uses a running local server or starts one |
| `FLYKE_TOKEN` | CLI | API token (admin token or a namespace access token) |
| `FLYKE_NAMESPACE` | CLI | namespace, default `default` |
| `FLYKE_PYTHON` | CLI | Python interpreter with `flyke` and `flyke-worker` installed |
| `FLYKE_KEEP_LOGS=1` | CLI | keep the local worker's log file after `flyke run` |
| `FLYKE_ADDR` | `flyke-server` | listen address, default `:8742` |
| `FLYKE_DATA_DIR` | `flyke-server` | database and bundle directory, default `./flyke-data` |
| `FLYKE_ADMIN_TOKEN` | server | require this bearer token on the REST API |
| `FLYKE_SECRET_KEY` | server | key that encrypts secrets at rest; default: `secret.key` in the data dir |
| `FLYKE_JOIN_TOKEN` | `flyke-worker` | join token, instead of `--token` |
| `FLYKE_WORKER_HOME` | `flyke-worker` | state directory (credential, bundle cache), default `~/.flyke-worker` |

---

## Changing the protocol

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
# edit api/proto/flyke/worker/v1/worker.proto
make gen      # buf lint + buf generate → api/gen
make lint     # buf breaking against the git history
```

See [protocol.md](protocol.md) for the compatibility rules.

## Troubleshooting

| Symptom | Fix |
|---|---|
| *no Python interpreter with `flyke` and `flyke-worker` installed was found* | run `make dev`, activate `.venv`, or set `FLYKE_PYTHON=$PWD/.venv/bin/python` |
| *dashboard not built into this binary* | run `make build` (it builds the UI first), not a bare `go build` |
| Runs stay `Pending` | no worker is connected to the run's pool: `flyke workers ls` |
| Docker worker can't find the workflow file | add `--bundle` to `flyke run` |
| Port 8742 is taken | another dev server runs; `flyke run` picks a free port, or stop it, or use `--addr` |
| Stale state after a schema change | delete the data dir (`rm -rf ./flyke-data`, or `docker compose down -v`) |
