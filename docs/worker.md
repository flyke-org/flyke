# The Python worker (`flyke-worker`)

A worker is the process that runs your code. The control plane decides **what** runs and
**when**; a worker connects to it, asks for work, runs it, and reports what happened. This
page explains how the Python worker does that and why it is built this way.

- [Running a worker](#running-a-worker)
- [Inside the worker](#inside-the-worker)
- [What happens to one item of work](#what-happens-to-one-item-of-work)
- [Where the code comes from](#where-the-code-comes-from)
- [Logs, secrets and redaction](#logs-secrets-and-redaction)
- [Cancellation, drain and shutdown](#cancellation-drain-and-shutdown)
- [Why the worker is built this way](#why-the-worker-is-built-this-way)
- [Limits today](#limits-today)

---

## Running a worker

```bash
pip install flyke==0.1.0.dev0 flyke-worker==0.1.0.dev0   # or, from a checkout: pip install ./sdk ./workers/python

flyke pools token builders --ttl 1h # on a machine with the CLI: prints a join token
flyke-worker join --url https://flyke.example.com --token flyke_jt_… --labels region=eu
```

| Command | What it does |
|---|---|
| `flyke-worker join --url … --token …` | registers with a join token, saves the credential, starts |
| `flyke-worker start` | starts again with the saved credential (after a reboot, in a systemd unit) |
| `flyke-worker leave` | deregisters and deletes the saved credential |

| Option | Default | Meaning |
|---|---|---|
| `--slots N` | CPU count (`$FLYKE_WORKER_SLOTS`) | how many items run at once |
| `--labels k=v,…` | none | labels the scheduler can match (`@task(pool=…)`, pool selectors) |
| `--name` | hostname + suffix | worker name |
| `--ephemeral` | off | don't save the credential; deregister on exit (containers, CI, dev) |
| `--state-dir` | `~/.flyke-worker` (`$FLYKE_WORKER_HOME`) | credential (mode 0600) and bundle cache |
| `--force` | off | register again even if a credential is saved |
| `--no-start` | off | register only |
| `-v` | off | debug logging |

With Docker:

```bash
docker build -f workers/python/Dockerfile -t flyke-worker .
docker run -d --restart=always -v flyke-worker:/var/lib/flyke-worker \
  flyke-worker join --url https://flyke.example.com --token flyke_jt_…
```

The image runs as a non-root user and keeps its state in the `/var/lib/flyke-worker` volume,
so a restarted container reuses its credential.

> [!TIP]
> To run several workers on the same host, give each one its own `--state-dir`. Usually one
> worker with more `--slots` is simpler.

## Inside the worker

```
flyke-worker
 ├─ cli.py        join | start | leave - labels, slots, capacity (CPU, memory)
 ├─ protocol.py   Connect client: HTTP/1.1 + JSON over httpx (the only file that knows the wire format)
 ├─ agent.py      heartbeat loop - poll loop - one job per assignment - drain - revocation
 ├─ bundles.py    fetch by digest → verify sha256 → safe tar extraction → on-disk cache
 └─ logs.py       per-attempt sinks - stdout/stderr capture - batched PushLogs - redaction
```

The agent runs on one asyncio event loop with three concurrent parts:

```
                 ┌─────────────────────────── agent event loop ───────────────────────────┐
                 │                                                                        │
                 │   heartbeat loop ── every 5s ──▶ Heartbeat(free slots, active leases)  │
                 │        ▲                         ◀── cancel_leases, drain               │
                 │        │ cancels                                                       │
                 │   poll loop ── free slots? ──▶ Poll(max_items = free, wait = 20s)      │
                 │        │                       ◀── assignments                          │
                 │        ▼                                                               │
                 │   one asyncio job per assignment ──▶ thread pool (slots + 2 threads)   │
                 │                                        │                               │
                 │   log shipper ── every 200ms ──▶ PushLogs(batches of ≤ 500)            │
                 └────────────────────────────────────────┼───────────────────────────────┘
                                                          ▼
                          ┌─────────────┐  ┌─────────────┐  ┌─────────────┐
                          │ thread      │  │ thread      │  │ thread      │
                          │ own event   │  │ own event   │  │ own event   │
                          │ loop: task  │  │ loop: step  │  │ loop: task  │
                          └─────────────┘  └─────────────┘  └─────────────┘
```

- The **poll loop** only polls when a slot is free, and asks for at most that many items.
  The worker is never given more than it can run.
- Each item runs in a thread from a pool of `slots + 2` threads, on **its own event loop**.
  Blocking code in one task (a synchronous HTTP client, `time.sleep`, a CPU-heavy loop) does
  not stall heartbeats, polling or other tasks.
- User code is loaded with `flyke.runtime.load(root, entrypoint)`, cached per file
  modification time, so local edits are picked up without restarting the worker.

## What happens to one item of work

### A workflow step

```
assignment (step, history)
  → resolve the code root (local path, or bundle from cache / FetchBundle)
  → in a thread: load the workflow, replay_step(definition, inputs, history)
  → flush logs
  → CompleteStep(lease, commands)   or   FailStep(lease, failure)
```

Replay is done by the SDK (`flyke.runtime.replay_step`): it re-runs `run()` against the
history and returns the new commands (schedule a task, start a timer, wait for an event,
emit an event, complete or fail the run). See [protocol.md](protocol.md#steps-replay-the-workflow-body).
Logs written by `run()` are shipped only the first time a line is reached, not on replay.

### A task

```
assignment (task, args, attempt, idempotency key, deadline)
  → AckTask(lease)        proceed = false?  → skip: the control plane moved on
  → in a thread with its own loop:
        execute_task(...), with asyncio.wait_for(deadline)
        stdout/stderr and self.log captured into this attempt's log sink
  → flush logs
  → CompleteTask(lease, result as JSON)   or   FailTask(lease, failure)
```

- The worker **never retries**. It reports the failure (type, type hierarchy, message, stack)
  and the control plane decides.
- A task past its deadline is cancelled and reported as a `TimeoutError`.
- A result that cannot be serialised to JSON is reported as a failure.
- Any unexpected error while handling an item is logged and never kills the agent.

## Where the code comes from

Each assignment says where the workflow's code is:

| Source | When | How the worker gets it |
|---|---|---|
| `local_root` | `flyke run` against a local control plane, as admin | reads the directory in place; edits apply on the next step |
| `bundle_digest` | `flyke deploy`, `flyke run --bundle`, any remote server | `FetchBundle(digest)`, verifies the sha256, extracts into `<state-dir>/bundles/<digest>`, caches it |

Bundles are **content-addressed**: the CLI builds a deterministic tar.gz (sorted files,
zeroed timestamps and owners; `.git`, `.venv`, caches and dot-directories skipped), so the
same code always has the same digest and is downloaded once per worker. Extraction refuses
absolute paths, `..`, links and device files.

> [!NOTE]
> A bundle contains source files only. Third-party packages your workflow imports must be
> installed in the worker's Python environment (`pip install -r requirements.txt`).

## Logs, secrets and redaction

```
self.log.info("built", tag=…)  ─┐
print(...) / stderr            ─┤  captured per thread, tagged with run, work item and attempt
                                ▼
in-memory buffer (≤ 50 000 records) → batches of ≤ 500 every 200ms → PushLogs
                                      flushed before every outcome is reported
                                      kept and retried if the push fails
```

Records carry a per-attempt sequence number, and the control plane deduplicates on
(work id, attempt, seq). A retried push never creates duplicate lines.

When a task reads a secret (`secrets["API_KEY"]`), the worker calls `GetSecret` with the
task's lease. The control plane returns it only for a task lease held by that worker, and
only from the run's namespace. Values marked **sensitive** are added to that attempt's
redactor, which masks them in log messages, structured fields, failure messages and stack
traces before anything leaves the worker.

## Cancellation, drain and shutdown

| Trigger | What the worker does |
|---|---|
| a lease appears in a heartbeat's `cancel_leases` (run cancelled, lease reassigned) | cancels that task's asyncio task; a late result is dropped |
| **SIGTERM / SIGINT**, or the drain flag (`flyke workers drain <worker>`) | stops polling (an in-flight long-poll is cancelled), finishes running work, deregisters if ephemeral, exits 0 |
| a **second** SIGTERM / SIGINT | cancels running work and exits |
| any call answers `unauthenticated` (`flyke workers rm`) | cancels everything, exits with code 3 |

To replace a worker host safely: `flyke workers drain <worker>` (or send SIGTERM), wait for
it to exit, then start the new one. Work it had not acknowledged is given to other workers;
work it acknowledged but could not finish is handled by each task's delivery guarantee.

## Why the worker is built this way

**A separate, small agent per language.** The control plane is Go; workers are written in
the language of the user's code, because only that language can run it. The protocol is
plain HTTP + JSON, so the Python worker needs one dependency (`httpx`) on top of the SDK,
and a worker for another language is a small project, not a port of the orchestrator.

**Stateless between items.** A step carries the whole history, and a task carries its
arguments. The worker keeps nothing a run needs between items, apart from a cache of
bundles. Any worker can take any item, workers can be added and removed at any time, and a
crashed worker loses nothing that the control plane does not already have.

**Threads with their own event loops.** User code is often not well-behaved async code. One
event loop per item, in its own thread, keeps a blocking task from freezing the heartbeat:
a worker that stops heartbeating would lose its leases and have its work reassigned.

**Facts, not decisions.** The worker reports what happened; retries, backoff, guarantees and
compensations are decided by the control plane and recorded in history. Behaviour is the
same whatever worker runs the code, and a worker crash never loses a decision.

**Outbound only, with revocable credentials.** The worker needs no open port. It joins with a
short-lived join token, gets its own credential, and can be cut off individually with
`flyke workers rm`.

## Limits today

- **No isolation between tasks.** All tasks share one Python process. A task can crash or
  block the worker, and the worker is not a security boundary between tenants. Process and
  container runtimes are planned.
- **Dependencies are not installed per bundle.** Install them in the worker's environment.
- **Logs are kept in memory** until pushed. A worker restart during a control-plane outage
  loses unpushed lines.
