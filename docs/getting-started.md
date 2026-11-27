# Getting started with Flyke

This guide takes you from nothing to a workflow running on a worker on another
machine. It takes about 15 minutes. You will:

1. Install the `flyke` CLI and the Python packages
2. Write a workflow and run it on your laptop
3. Watch it in the dashboard
4. Start a shared control plane and add a worker on another machine

You only need to know some Python (`async`/`await`) and be comfortable in a terminal.

---

## How Flyke works

There are two parts:

- **The control plane** (`flyke server`) stores your workflows and runs and
  decides what runs next. It serves the API and the dashboard.
- **Workers** (`flyke-worker`) run your Python code. A worker connects *out* to
  the control plane and asks it for work, so it never needs an open inbound port.

On your laptop you don't need to set up either one: `flyke run` starts both for
you. Later, when you want your code to run somewhere else, you start a real
control plane and connect workers to it. Both setups use the same protocol, so a
workflow that runs locally runs the same way remotely.

---

## 1. Install

### Prerequisites

| Tool | Version | Used for |
|---|---|---|
| Linux or macOS | amd64 or arm64 | the prebuilt `flyke` binary |
| Python | 3.11 or newer | your workflows and the worker |
| curl | any | the install script |

### Install the `flyke` CLI

```bash
curl -fsSL https://raw.githubusercontent.com/flyke-org/flyke/main/install.sh | sh
flyke --version
```

### Create your project and install the Python packages

Create a folder with a virtual environment and install the SDK (`flyke`) and the worker
(`flyke-worker`) into it:

```bash
mkdir ~/my-flows && cd ~/my-flows
python3 -m venv .venv
source .venv/bin/activate
pip install flyke==0.1.0.dev0 flyke-worker==0.1.0.dev0
```

> Flyke only has development releases so far, so pin the version. Avoid `pip install --pre`:
> it also pulls prereleases of dependencies such as `httpx`.

> `flyke` finds your Python on its own. It checks `$FLYKE_PYTHON`, then the
> active virtualenv, then a `.venv` folder next to your workflow file or in any
> folder above it. If you see *"no Python interpreter with `flyke` and
> `flyke-worker` installed was found"*, activate the venv or set
> `FLYKE_PYTHON=/path/to/.venv/bin/python`.

---

## 2. Write and run your first workflow

Create `~/my-flows/hello.py`:

```python
from flyke import workflow, task, Retry


@workflow
class Hello:
    name: str = "world"  # fields are the workflow's inputs

    @task(retry=Retry(attempts=3))
    async def greet(self) -> str:
        self.log.info("greeting", name=self.name)
        return f"hello {self.name}"

    async def run(self) -> str:  # the workflow body
        return await self.greet()
```

What each part does:

- `@workflow` turns a class into a workflow. Its fields are its **inputs**,
  and `async def run(self)` is the body.
- `@task` marks one step. Flyke records each task's result. If the workflow is
  interrupted, finished tasks are **not run again**: their recorded results are
  reused. `Retry(attempts=3)` retries the task up to three times if it fails.
- `self.log` writes structured logs, which you can see in the terminal and the
  dashboard.

Run it:

```bash
flyke run hello.py:Hello --name=Ada -f
```

Expected output:

```
local control plane on http://127.0.0.1:8742 (ephemeral) - 1 local worker
▶ hello-23i35ct6 (Hello v-001cdb55101d)
11:38:19.024 flyke          INFO run created triggered_by=api version=v-001cdb55101d
11:38:19.025 flyke          INFO run admitted
11:38:19.201 greet          INFO greeting name=Ada
11:38:19.202 flyke          INFO task greet succeeded attempt=1
11:38:19.204 flyke          INFO run succeeded

hello-23i35ct6 Succeeded in 2s
"hello Ada"
```

- Each workflow field becomes a flag: `--name=Ada`. Run
  `flyke run hello.py:Hello --nope` to see the list of valid inputs.
- `-f` (`--follow`) streams the logs. Without it, `flyke run` only prints the
  result.
- With nothing else running, `flyke run` starts a **temporary** control plane
  and worker, and both stop when the run ends.

---

## 3. Use the dashboard

To keep your runs after they finish and see them in a browser, start a
long-running local environment in a second terminal:

```bash
cd ~/my-flows && source .venv/bin/activate
flyke server --dev
```

Open http://127.0.0.1:8742. Now go back to your first terminal and run the
workflow again:

```bash
flyke run hello.py:Hello --name=Ada -f
```

`flyke run` finds the dev server by itself and prints a link to the run. The
dashboard shows the workflow's diagram, each task's inputs, output, attempts and
logs, and buttons to retry or cancel.

You can do the same from the terminal:

```bash
flyke runs ls                 # recent runs
flyke runs get <run>          # one run and its tasks
flyke logs -f <run>           # stream a run's logs
flyke retry <run>             # re-run only what failed, reusing what succeeded
```

> `flyke server --dev` keeps data in memory, so restarting it clears your
> runs. Add `--data-dir ./flyke-data` to keep them.

Stop the dev server (Ctrl-C) before moving on.

---

## 4. Add a remote worker

Now let's run your code on a different machine: a VM, a build box, or anything
that can reach your control plane over the network. You need:

- **Machine A**: runs the control plane. Your laptop works for this test.
- **Machine B**: runs the worker. It must be able to reach Machine A on port 8742.

### 4.1 Start a shared control plane (Machine A)

`flyke server` without `--dev` is the real control plane. It listens on all
interfaces (`:8742`), saves its state to disk, and starts **no** local worker,
so every task runs on workers you connect yourself.

```bash
export FLYKE_ADMIN_TOKEN=$(openssl rand -hex 32)
echo "$FLYKE_ADMIN_TOKEN"     # keep this: the CLI needs it to talk to the server

flyke server --data-dir ./flyke-data
```

The admin token protects the API and dashboard. Without it, anyone who can reach
the port can control your workflows.

In another terminal (on Machine A, or any machine with the CLI), point the CLI
at the server. Use an address that Machine B can also reach, not `127.0.0.1`:

```bash
export FLYKE_SERVER=http://<machine-a-address>:8742
export FLYKE_TOKEN=<the admin token>
flyke workers ls             # empty for now
```

### 4.2 Create a join token

Workers belong to **pools**. A join token lets a machine register itself into
one pool. If the pool doesn't exist yet, it is created:

```bash
flyke pools token workers --ttl 1h
```

```
join token for pool workers (expires 2026-11-09T12:38:34+01:00):

  flyke_jt_zo458y.q919fpfee7g31pzdezstz4mb

register a host with:

  flyke-worker join --url http://192.168.1.27:8742 --token flyke_jt_zo458y.q919fpfee7g31pzdezstz4mb
```

The token is only used for registration, and it expires (`--ttl`). You can also
limit how many machines can use it with `--max-uses 1`.

### 4.3 Install and join the worker (Machine B)

On Machine B, install Python 3.11+ and the worker, the same way as in step 1:

```bash
python3 -m venv ~/flyke-worker-env
~/flyke-worker-env/bin/pip install flyke==0.1.0.dev0 flyke-worker==0.1.0.dev0
```

Then run the `join` command that step 4.2 printed:

```bash
~/flyke-worker-env/bin/flyke-worker join \
  --url http://<machine-a-address>:8742 \
  --token flyke_jt_… \
  --labels region=lab
```

```
INFO flyke.worker: registered as machine-b-eyl7p with labels {'flyke.io/pool': 'workers', 'region': 'lab'}
INFO flyke.worker: worker machine-b-eyl7p ready (10 slots)
```

When the worker registers, it gets its own credential and saves it to
`~/.flyke-worker/credentials.json` (readable only by you). After that it no
longer needs the join token:

```bash
flyke-worker start     # restart later with the saved credential
flyke-worker leave     # deregister this machine
```

Useful options: `--slots N` sets how many tasks run at once (default: number of
CPUs), and `--labels k=v,...` adds labels you can use to target machines.
Stopping the worker with Ctrl-C or SIGTERM lets running work finish first.

### 4.4 Check that it joined (Machine A)

```bash
flyke workers ls
```

```
NAME             STATUS  POOL     BUSY  RUNTIMES     HEARTBEAT  LABELS
machine-b-eyl7p  Ready   workers  0/10  python/3.12  3s         flyke.io/pool=workers,region=lab
```

### 4.5 Run your workflow on the remote worker

From your project on Machine A, with `FLYKE_SERVER` and `FLYKE_TOKEN` still set:

```bash
cd ~/my-flows
flyke run hello.py:Hello --name=remote -f
```

```
11:38:44.397 greet          INFO greeting name=remote
11:38:44.398 flyke          INFO task greet succeeded attempt=1
11:38:44.400 flyke          INFO run succeeded

hello-atqvv1ys Succeeded in 2s
"hello remote"
```

You don't copy your code to Machine B yourself. `flyke run` packs the
workflow's folder into a **bundle** and uploads it to the control plane, and the
worker downloads it, checks it, and caches it.

To register a workflow without starting a run (for example, so that it can be
started from the dashboard or by its triggers), use:

```bash
flyke deploy hello.py:Hello
```

### Send work to a specific pool

Any worker can pick up a workflow that doesn't name a pool. When you have
several pools (for example `gpu` machines and general `workers`), you choose
where code runs with `pool=`:

```python
@workflow(pool="workers")  # the whole workflow runs in this pool
class Hello:
    ...

    @task(pool="gpu")  # or only one task
    async def embed(self) -> list[float]: ...
```

---

## Things to know about remote workers

- **Third-party packages are not shipped.** The bundle only contains your
  project's files. If your workflow imports `requests` or `numpy`, install them
  in the worker's environment on every worker machine.
- **Use the same Python minor version** on workers as on the machine you run
  `flyke run` from, so your code behaves the same everywhere.
- **Networking:** only workers connect to the control plane, never the other
  way. Open port 8742 on Machine A to your workers, and to the people who use
  the CLI and dashboard.
- **Use HTTPS** outside a trusted network. Put the control plane behind a
  reverse proxy that handles TLS (Caddy, nginx, a cloud load balancer) and
  use `https://` URLs for `--url` and `FLYKE_SERVER`.

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `no Python interpreter with flyke and flyke-worker installed was found` | Activate your project's venv, or set `FLYKE_PYTHON` to its `python`. |
| `no control plane found` | Set `FLYKE_SERVER`, or start one with `flyke server --dev`. |
| `401` / `unauthorized` from the CLI | Set `FLYKE_TOKEN` to the server's admin token. |
| Worker `registration failed` | The join token expired or was used up. Create a new one with `flyke pools token <pool>`. |
| Run stays queued | No worker can take it: check `flyke workers ls`, and check that the `pool=` you set matches a pool that has ready workers. |
| `ModuleNotFoundError` in a task on the remote worker | Install that package in the worker's environment (see above). |
| `dashboard not built into this binary` | Rebuild the CLI with `make build` (Node.js is required). |

## Next steps

- `examples/deploy.py` in the Flyke repository shows parallel steps,
  concurrency limits, compensations (undo steps), and waiting for a human
  approval with `wait_for` + `flyke emit`.
- `examples/rag.py` shows a fan-out data pipeline and `flyke retry --from`.
- `flyke <command> --help` lists every option.
