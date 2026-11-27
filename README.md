<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="branding/logo/flyke-logo-white-text.svg">
  <img src="branding/logo/flyke-logo.svg" alt="Flyke" width="340">
</picture>

### Durable workflows in plain async Python, from one binary.

Retries, sagas, waits for humans and replay after crashes, without a DAG or YAML.

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev)
[![Python](https://img.shields.io/badge/Python-3.11+-3776AB?style=flat-square&logo=python&logoColor=white)](https://www.python.org)
[![React](https://img.shields.io/badge/Dashboard-React_+_Vite-61DAFB?style=flat-square&logo=react&logoColor=black)](ui/)
[![SQLite](https://img.shields.io/badge/Storage-SQLite_built_in-003B57?style=flat-square&logo=sqlite&logoColor=white)](flyke/internal/adapter/store/sqlite)
[![Protocol](https://img.shields.io/badge/Protocol-Connect_/_gRPC-244c5a?style=flat-square)](docs/protocol.md)
[![License](https://img.shields.io/badge/License-ELv2_·_SDK_Apache--2.0-8A2BE2?style=flat-square)](LICENSING.md)
[![PRs welcome](https://img.shields.io/badge/PRs-welcome-brightgreen?style=flat-square)](CONTRIBUTING.md)

[**Getting started**](docs/getting-started.md) ·
[**Docs**](#-documentation) ·
[**Contributing**](CONTRIBUTING.md) ·

</div>

---

## 🚀 Quick start

> [!TIP]
> New to Flyke? **[docs/getting-started.md](docs/getting-started.md)** takes you from
> nothing to a workflow running on a remote worker in about 15 minutes.

### Install

**CLI** (Linux / macOS, amd64 / arm64). The `flyke` binary includes the control plane and
the dashboard:

```sh
curl -fsSL https://raw.githubusercontent.com/flyke-org/flyke/main/install.sh | sh
```

It installs the newest [release](https://github.com/flyke-org/flyke/releases) to
`/usr/local/bin`. Set `FLYKE_INSTALL_DIR=~/.local/bin` to install elsewhere, or
`FLYKE_VERSION=0.1.0-dev.0` to pin a version.

**Python SDK and worker** (Python 3.11+), in your project's virtual environment:

```sh
python3 -m venv .venv && source .venv/bin/activate
pip install flyke==0.1.0.dev0 flyke-worker==0.1.0.dev0
```

To build from source instead, see [docs/development.md](docs/development.md).


### Run the example

```python
@workflow(
  concurrency=Concurrency(key="{service}:{env}"),
  timeout="45m"
)
class DeployService:
    service: str = "api"
    env: Literal["staging", "prod"] = "staging"

    @task(retry=Retry(attempts=5, on=(ConnectionError,)))
    async def build(self) -> str: ...

    async def run(self) -> dict:
        image = await self.build()
        if self.env == "prod":
            await self.wait_for(
              "deploy.approved",
              where={"service": self.service},
              timeout="24h"
            )
        return {"image": image}
```

With nothing configured, `flyke run` starts an in-process control plane and a local
worker subprocess. They talk over the same protocol as production, so a workflow that
works on your laptop works the same way on remote workers.

```bash
curl -fsSLO https://raw.githubusercontent.com/flyke-org/flyke/main/examples/deploy.py

flyke run deploy.py:DeployService --service=api -f
flyke run deploy.py:DeployService --service=api --env=prod -f
flyke emit deploy.approved service=api user=you   # in another terminal

flyke run deploy.py:DeployService --service=unstable -f   # watch the rollback
```

## Why Flyke?

Flyke is a code-first workflow orchestrator for **application developers**. A workflow
is a Python class with an `async def run()`. Flyke records the result of every step, so a run
survives crashes, restarts and waits that last days, and continues from where it stopped.

<table>
<tr>
<td width="50%" valign="top">

#### 🐍 Plain Python
Fields are inputs, `@task` marks a step, and `run()` is ordinary code:
`if`, `for`, `asyncio.gather`. There is no DAG object and no operators.

</td>
<td width="50%" valign="top">

#### 💾 Durable by replay
Every step is recorded. After a crash, `run()` is replayed against the history
and finished tasks are **never run again**.

</td>
</tr>
<tr>
<td valign="top">

#### ⏳ Waits for humans and events
`wait_for("deploy.approved")` and `sleep("3d")` hold no process and no worker slot.
The wait is a row in the store.

</td>
<td valign="top">

#### ↩️ Sagas built in
`@task.compensate` undoes completed steps in reverse order when a run fails
for good.

</td>
</tr>
<tr>
<td valign="top">

#### 🎯 Delivery guarantees per task
`AT_LEAST_ONCE`, `EFFECTIVELY_ONCE` or `AT_MOST_ONCE`, with an option to
**pause and ask a person** when the outcome is unknown.

</td>
<td valign="top">

#### 📦 One binary, nothing to install
The control plane, SQLite storage and the dashboard are in one Go binary.
`flyke run` starts everything locally.

</td>
</tr>
</table>

## Contents

- [How it works](#-how-it-works)
- [Features](#-features)
- [Production shape](#-production-shape)
- [Repository layout](#-repository-layout)
- [Documentation](#-documentation)
- [Development](#-development)
- [Contributing](#-contributing)
- [License](#-license)


## 🧠 How it works

```
          flyke CLI            dashboard            your app
              │                    │                    │
              └────────────────────┼────────────────────┘
                                   │  REST
                                   ▼
             ╔═════════════════════════════════════════════╗
             ║         CONTROL PLANE  -  flyke server      ║
             ║                                             ║
             ║   runs - history - scheduler - triggers     ║
             ║   secrets - namespaces - embedded UI        ║
             ║   ─────────────────────────────────────     ║
             ╚═════════════════════════════════════════════╝
                                   ▲
                                   │  flyke.worker.v1  (Connect over HTTP/JSON, or gRPC)
                                   │
              ┌────────────────────┼────────────────────┐
              │                    │                    │
       ┌──────┴──────┐      ┌──────┴──────┐      ┌──────┴──────┐
       │   worker    │      │   worker    │      │   worker    │
       │  pool=main  │      │  pool=main  │      │  pool=gpu   │
       └─────────────┘      └─────────────┘      └─────────────┘
```

The **control plane** stores workflows and runs and decides what runs next.
**Workers** run your Python code. They connect *out* to the control plane and ask it for work.

The workflow body is **replayed**. Each time something happens (a task finishes, a timer
fires, an event arrives), a worker runs `run()` from the top against the recorded history

No worker holds a run's state in memory, so any worker can pick up any step and losing a
worker loses nothing. The one rule: **`run()` must be deterministic**. Do I/O in tasks and
use `self.now()` / `self.sleep()` instead of the clock. If the code and the history disagree,
Flyke pauses the run with a `NonDeterminismError` instead of failing it, so you can fix the
code and `flyke resume`.

## ✨ Features

### Delivery guarantees

A worker can die in the middle of a task, and the orchestrator then cannot know whether the
side effect happened. Flyke makes you choose, per task:

| Guarantee | If the worker dies mid-task | Use it for |
|---|---|---|
| `AT_LEAST_ONCE` *(default)* | the task runs again | reads, idempotent calls |
| `EFFECTIVELY_ONCE` | runs again with the same idempotency key; a task that already succeeded with that key is not run again | payments, e-mails, provisioning |
| `AT_MOST_ONCE` | never re-run automatically; the run fails, or pauses until a person decides | migrations, anything you cannot repeat |

```python
@task(guarantee=AT_MOST_ONCE, timeout="10m", on_unknown="pause")
async def migrate_db(self, image: str) -> None: ...
```

```bash
flyke task resolve <run> <ref> --outcome=succeeded    # or failed, or retry
```

### Compensations (sagas)

```python
@task(guarantee=EFFECTIVELY_ONCE, idempotency_key="{service}-{env}-{image}-{region}")
async def provision(self, image: str, region: str) -> str: ...


@provision.compensate
async def deprovision(self, image: str, region: str, result: str) -> None: ...
```

```
WARN task smoke_test failed: RuntimeError: 502 from load balancer (3/3 attempts exhausted)
WARN run failed: TaskError — compensating completed tasks
WARN deprovision  rolling back region=eu-west-1
WARN deprovision  rolling back region=us-east-1
INFO compensation completed; run Failed
```

### Retry without redoing what worked

A retry is a new run, linked to the original, that reuses recorded results:

```bash
flyke retry <run>                            # re-run only failed / not-reached steps
flyke retry <run> --from embed_document      # reuse everything before it, re-run it and after
flyke retry <run> --all --set chunk_size=4   # everything again, with other inputs
```

A result is reused only when the task is called at the same position with the same
arguments, and never if it was compensated. Fixed code or changed inputs therefore
re-run whatever they affect. The dashboard offers the same choices, with a preview of
what will be reused.

### Schedules

```bash
flyke schedules create nightly Report --daily 03:00 --tz Europe/Paris
flyke schedules create sync Sync --every 15m --set source=crm
flyke schedules create weekdays Digest --cron "0 9 * * 1-5" --set day={schedule.date}
flyke schedules ls                       # what runs when, next and last run
flyke schedules pause nightly            # resume | trigger (run now) | update | rm
flyke schedules explain "0 9 * * 1-5"    # plain English + next runs, nothing saved
```

or declare them next to the code, and `flyke deploy` creates them:

```python
@workflow(triggers=[on.schedule("0 3 * * *", tz="Europe/Paris"), on.every("15m", name="refresh")])
```

Every schedule is shown in plain English with its next runs (*"Monday to Friday at 09:00
(Europe/Paris)"*). Times are UTC unless you set a time zone. The scheduler sleeps until the
next run is due instead of polling, so idle schedules cost nothing.

### The dashboard

`flyke server --dev` starts a long-lived local control plane and worker and serves the
dashboard at **http://127.0.0.1:8742**. `flyke run` attaches to it automatically and prints
each run's URL.

- **Diagrams** of each workflow, generated from `run()`: parallel `gather` branches,
  fan-outs, conditional steps. The run page overlays live status; click any step to see its
  inputs, output, error, attempts and logs.
- **Runs**: live task timeline, logs, approvals (`wait_for`), resolution of unknown outcomes.
- **Workflows** with generated start forms, **schedules**, **events**, **workers**, **pools**
  and **join tokens**.

### And more

| | |
|---|---|
| ⚡ **Event triggers** | `on.event("git.pushed", where={"branch": "main"})` starts a run when your app or another workflow emits the event |
| 🔐 **Secrets & variables** | `flyke secrets set API_KEY --sensitive`, then `secrets["API_KEY"]` in a task. Sensitive values are encrypted at rest and never returned by the API |
| 🚦 **Concurrency** | `Concurrency(key="{service}:{env}", limit=1, on_conflict="queue")`: queue, skip or cancel overlapping runs |
| 🌍 **Remote workers** | `flyke-worker join --url … --token …` on any machine. Your code is uploaded as a bundle |
| 🧩 **Pools & placement** | `@task(pool="gpu")` sends one task to a group of machines |
| 🏢 **Namespaces** | each team gets its own tokens, workers, pools, code and secrets |

## 🏭 Production shape

```bash
flyke server --data-dir /var/lib/flyke --admin-token "$TOKEN"        # control plane
flyke pools create builders --profile heavy=cpu:4,memory:8Gi
flyke pools token builders --ttl 1h                                   # prints the join command
flyke-worker join --url https://flyke.example.com --token flyke_jt_…  # on each worker host
flyke deploy examples/deploy.py:DeployService                          # upload code, enable triggers
```

Day-to-day operations:

```bash
flyke workers ls
flyke runs ls
flyke logs -f <run>
flyke retry <run> [--from <task>]
flyke task resolve <run> <ref> --outcome=…
```

## 🗂 Repository layout

| Path | Depends on |
|---|---|
| `api/` | — |
| `flyke/` | `api` |
| `cli/` | `api`, `flyke/server` |
| `sdk/` | stdlib only |
| `workers/python/` | `sdk`, `httpx` |
| `ui/` | REST API only |

The server and the workers share nothing but `api/`. Any process that speaks
`flyke.worker.v1` is a worker: unary Connect RPCs over HTTP/1.1+JSON, or gRPC.
See **[docs/protocol.md](docs/protocol.md)**.

## 📚 Documentation

| Guide | Read it to… |
|---|---|
| [Getting started](docs/getting-started.md) | install Flyke, write a workflow, add a remote worker (15 min) |
| [The CLI](docs/cli.md) | learn every command, how local mode works, what `flyke run` does |
| [The worker](docs/worker.md) | understand how your code is run, cancelled and drained, and why |
| [The protocol](docs/protocol.md) | see how workers and the control plane talk, or write a worker in another language |
| [Developing Flyke](docs/development.md) | hack on Flyke, with Docker Compose or without |

## 🛠 Development

Full guide: **[docs/development.md](docs/development.md)**.

```bash
make test        # go vet + go test -race (all Go modules), pytest, UI typecheck
make fmt         # ruff lint + format (the same hooks run on every commit)
make ui-dev      # dashboard with hot reload on :5173 (run `flyke server --dev` alongside)
make gen         # regenerate protocol code after editing api/proto
```

<details>
<summary><b>🐳 Docker Compose (live reload)</b></summary>

<br>

```bash
docker compose up --build
```

The source tree is bind-mounted into the containers, so edits apply without rebuilding
images: `flyke/` and `api/` changes rebuild and restart the server (air), `ui/` changes
hot-reload in the browser, and `sdk/` or `workers/python/` changes restart the worker. Run
workflows from the host with `--bundle`, so the code is uploaded instead of passed as a host
path the worker can't see:

```bash
FLYKE_SERVER=http://127.0.0.1:8742 flyke run examples/hello.py:Hello --bundle -f
```

`docker compose down -v` also wipes the server's data volume.

</details>

## 🤝 Contributing

Bug reports, docs, examples and code are all welcome. Read
**[CONTRIBUTING.md](CONTRIBUTING.md)** for how to set up, test and submit a change. For
anything bigger than a small fix, open an issue first so we can agree on the approach.
Report security issues privately, not in a public issue.

## 📄 License

| Part | License |
|---|---|
| Control plane, CLI, dashboard and everything else | [Elastic License 2.0](LICENSE) |
| Python SDK (`sdk/`), Python worker (`workers/python/`), protocol (`api/`) | [Apache 2.0](sdk/LICENSE) |

You can use, modify and self-host Flyke for free, including in commercial products. You
can't offer it to others as a hosted or managed service. The SDK, the worker and the
protocol are Apache-2.0, so nothing in your own code is restricted. Details and examples
are in **[LICENSING.md](LICENSING.md)**.

---

<div align="center">

**[Get started →](docs/getting-started.md)**

<sub>Built with Go, Python and React.</sub>

</div>
