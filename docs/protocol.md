# The worker protocol (`flyke.worker.v1`)

The control plane and the workers share exactly one thing: this protocol. It is defined in
[`api/proto/flyke/worker/v1/worker.proto`](../api/proto/flyke/worker/v1/worker.proto) and is
licensed Apache-2.0, so anyone can implement a worker for it, in any language.

This page explains **how it works**, **why it is designed this way**, and **how to write a
worker** for it.

- [The short version](#the-short-version)
- [Transport](#transport)
- [Lifecycle of a worker](#lifecycle-of-a-worker)
- [Two kinds of work: steps and tasks](#two-kinds-of-work-steps-and-tasks)
- [Leases, acks and fencing](#leases-acks-and-fencing)
- [RPC reference](#rpc-reference)
- [Why it looks like this](#why-it-looks-like-this)
- [Writing a worker in another language](#writing-a-worker-in-another-language)
- [Compatibility rules](#compatibility-rules)

---

## The short version

```
 worker                                           control plane
 ──────                                           ─────────────
 Register(join_token)               ───────────▶  verify token, create worker
                                    ◀───────────  credential, heartbeat interval, lease TTL

 loop every 5s:
   Heartbeat(free_slots, leases)    ───────────▶  renew leases
                                    ◀───────────  cancel_leases, drain

 loop:
   Poll(max_items, wait_seconds)    ───────────▶  (holds the request until work is ready)
                                    ◀───────────  assignments [ step | task ]

   step → replay run() on the history
   CompleteStep(lease, commands)    ───────────▶  record commands, schedule tasks/timers/waits

   task → AckTask(lease)            ───────────▶  assigned → started
                                    ◀───────────  proceed = true
          run user code
          CompleteTask(lease, result)  or  FailTask(lease, failure)  ──▶  record, decide retries
```

Four rules shape everything:

1. **Workers dial out, never in.** The control plane never opens a connection to a worker.
2. **Unary RPCs and long-polling only.** No streams, so any HTTP client can be a worker.
3. **Leases, not trust.** Every assignment carries a lease id. Every report is checked against
   it, so a worker that lost its lease cannot overwrite newer work.
4. **Workers report facts; the control plane decides.** Retries, backoff, compensation and
   timeouts are all decided on the server.

## Transport

The service is served with [Connect](https://connectrpc.com), so the **same endpoint** speaks
three protocols:

| Protocol | Use it when |
|---|---|
| **Connect, HTTP/1.1 + JSON** | you want the smallest client: `POST /flyke.worker.v1.WorkerService/<Method>` with a JSON body. The Python worker uses this, with `httpx` only. |
| **gRPC** (HTTP/2, h2c accepted) | your language has a good gRPC stack and you want generated clients |
| **gRPC-Web** | browsers and some proxies |

Every request except `Register` sends the worker credential as
`Authorization: Bearer <credential>`.

JSON bodies use the standard protobuf JSON mapping: field names in `lowerCamelCase`, `bytes`
fields base64-encoded, timestamps as RFC 3339 strings.

```bash
curl -s http://127.0.0.1:8742/flyke.worker.v1.WorkerService/Poll \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $CREDENTIAL" \
  -d '{"maxItems": 1, "waitSeconds": 20}'
```

Errors are Connect errors: an HTTP status plus `{"code": "...", "message": "..."}`.

| Code | Meaning for the worker |
|---|---|
| `unauthenticated` | the credential is unknown or revoked: stop everything and exit |
| `failed_precondition`, `not_found` | the lease is stale (expired, cancelled or reassigned): drop the result |
| `unavailable`, `deadline_exceeded`, `internal`, `resource_exhausted`, `aborted` | transient: retry with backoff |
| `invalid_argument` | a bug in the worker: the request was malformed |

### Payloads

Inputs, arguments and results are opaque to the protocol:

```proto
message Payload {
  string encoding = 1;   // "json/plain" is the only built-in codec today
  bytes data = 2;        // the encoded value (base64 in JSON)
}
```

The control plane never looks inside a payload, apart from reading workflow inputs and
event fields as JSON for filters and concurrency keys. A future codec (for example
encrypted payloads) only needs both ends to agree on a new `encoding`.

## Lifecycle of a worker

```
  ┌──────────┐  Register   ┌───────┐  no heartbeat for 15s   ┌──────────┐
  │  (none)  │ ──────────▶ │ Ready │ ──────────────────────▶ │ NotReady │
  └──────────┘             └───┬───┘ ◀────────────────────── └──────────┘
                               │        heartbeat resumes
     drain (flyke workers      │
     drain, or SIGTERM)        ▼
                     ┌───────────────────┐  running work done,  ┌─────────┐
                     │ Ready + draining  │ ───────────────────▶ │ removed │
                     │ (no new work)     │  Deregister          └─────────┘
                     └───────────────────┘
```

A worker gets new work only when it is `Ready`, not draining and not cordoned
(`flyke workers cordon`).

**1. Bootstrap.** An operator creates a join token for a pool (`flyke pools token builders`).
The worker calls `Register` with it, plus its hostname, labels, capacity (slots, CPU,
memory), runtimes (for example `python/3.12`) and `protocol_version: "v1"`. The response
contains a **per-worker credential**, shown once. Join tokens can expire, be limited in uses,
and restrict which labels a worker may claim. Both join tokens and credentials are stored
as sha256 hashes.

**2. Heartbeats.** Every `heartbeat_interval_seconds` (default 5s) the worker sends its free
slots and the lease ids it is running. The response lists leases to **cancel** (the run was
cancelled, or the lease was reassigned) and may ask the worker to **drain**. After 15s of
silence the worker is marked `NotReady` and gets no new work; its leases expire after their
TTL (30s) and the work is handled according to each task's guarantee.

**3. Polling.** The worker asks for at most as many items as it has free slots. The control
plane holds the request open (up to 30s) until work that fits is ready. Scheduling happens
**at poll time**: the server runs its filter chain (schedulable, runtime match, pool
selector, pool slots, resources fit) for the polling worker only.

**4. Leaving.** On SIGTERM or a drain request, the worker stops polling, finishes running
work, then calls `Deregister`, which revokes its credential. `flyke workers rm` revokes it
from the server side; the worker's next call gets `unauthenticated` and it exits.

## Two kinds of work: steps and tasks

Every `Assignment` is one of two kinds:

```proto
message Assignment {
  string lease_id = 1;
  string run_id = 2;
  ...
  Source source = 5;          // entrypoint + local_root or bundle_digest
  Payload inputs = 6;         // the workflow's inputs
  google.protobuf.Timestamp deadline = 7;
  oneof work {
    TaskWork task = 10;       // run one @task (or compensation)
    StepWork step = 11;       // replay run() against the history
  }
}
```

### Steps: replay the workflow body

A **step** carries the run's full history (`RunStarted`, `TaskScheduled`, `TaskCompleted`,
`TimerFired`, `EventReceived`…). The worker loads the workflow code and re-executes `run()`
from the top:

- each durable call (task, timer, wait, emit) gets a sequence number, its **ref**, in call
  order;
- if the history already has the outcome for that ref, the call returns it immediately;
- otherwise the call becomes a **command** (`ScheduleTask`, `StartTimer`, `WaitEvent`,
  `EmitEvent`) and stays pending;
- when nothing else can progress, the step ends and the worker sends the new commands with
  `CompleteStep`. If `run()` returned, the command is `CompleteRun`; if it raised, `FailRun`.

If the code issues a different command for a ref than the history recorded, the worker
reports `FailStep` with a `NonDeterminismError`, and the control plane **pauses** the run
instead of failing it, so you can fix the code and `flyke resume`.

Steps are short (the deadline is 2 minutes) and side-effect free, so they are always safe
to run again. They do not need `AckTask`.

### Tasks: run user code once

A **task** carries the function name, the attempt number, the JSON arguments and an
idempotency key. A **compensation** is a task with `compensation: true`, called with the
original task's arguments followed by its result.

Before running user code, the worker **must** call `AckTask` and get `proceed: true`. Then
it reports exactly one outcome: `CompleteTask` with the result, or `FailTask` with a
`Failure`:

```proto
message Failure {
  string type = 1;              // "builtins.ConnectionError"
  repeated string mro = 2;      // type hierarchy, most specific first: used for Retry(on=…)
  string message = 3;
  string stack = 4;
  bool non_retryable = 5;
}
```

The worker does **not** retry. The control plane matches `mro` against the task's retry
policy, computes the backoff, and schedules the next attempt, possibly on another worker.

## Leases, acks and fencing

A lease id is the identity of **one attempt on one worker**. It does three jobs.

**Fencing.** Every report (`AckTask`, `CompleteTask`, `FailTask`, `CompleteStep`, `FailStep`,
`GetSecret`) names its lease. If the lease has expired, was cancelled or was reassigned,
the control plane rejects the report. A worker that was paused for a minute and wakes up
cannot overwrite the result of the attempt that replaced it.

**Liveness.** Heartbeats renew the leases of running work. A lease that is not renewed
expires, and the control plane knows the worker is gone.

**Knowing whether user code started.** `AckTask` separates "assigned" from "started":

```
 Poll response lost on the network         Worker dies after AckTask
 ─────────────────────────────────         ──────────────────────────
 assigned, never acked                     acked = user code may have run
 → lease expires after 15s (ack TTL)       → lease expires after 30s
 → safe to give to another worker          → outcome is UNKNOWN
                                           → the task's guarantee decides:
                                               AT_LEAST_ONCE     run again
                                               EFFECTIVELY_ONCE  run again, same idempotency key
                                               AT_MOST_ONCE      fail, or pause for a person
```

Without the ack, a lost poll response would look exactly like a crash in the middle of a
task, and an `AT_MOST_ONCE` task would be stuck for no reason.

## RPC reference

| RPC | Called | Request → response |
|---|---|---|
| `Register` | once, with a join token (the only call without a credential) | hostname, name, labels, capacity, runtimes, versions → worker name, credential, effective labels, heartbeat interval, lease TTL |
| `Heartbeat` | every heartbeat interval | free slots, active leases → leases to cancel, drain flag |
| `Poll` | in a loop, when slots are free | max items, wait seconds → assignments |
| `AckTask` | before running a task's code | lease → `proceed` |
| `CompleteTask` / `FailTask` | once per task attempt | lease + result / failure |
| `CompleteStep` / `FailStep` | once per step | lease + commands / failure |
| `PushLogs` | in batches, and before each outcome | log records (deduplicated on work id, attempt, seq) → accepted count |
| `FetchBundle` | when a bundle digest is not cached | digest → tar.gz (the worker verifies the sha256) |
| `GetSecret` | when a task reads a secret | lease, key → value, sensitive flag, type (only for a task lease held by this worker; scoped to the run's namespace) |
| `Deregister` | on graceful exit | revokes the credential |

### Timing defaults

| Setting | Default |
|---|---|
| heartbeat interval | 5s |
| worker marked `NotReady` after | 15s without heartbeat |
| ack TTL (assigned, not acked) | 15s |
| running lease TTL | 30s, renewed by heartbeats |
| max poll wait | 30s |
| step deadline | 2 minutes |

## Why it looks like this

### Why workers dial out

Workers often run where inbound connections are hard or forbidden: a laptop, a private
subnet, a customer's network, behind NAT. If the worker opens every connection, it needs
**no open port, no service discovery and no firewall rule**, only HTTPS access to the
control plane. It also makes the control plane simpler: it never tracks worker addresses.

### Why unary RPCs and long-polling instead of streams

The first design used one bidirectional gRPC stream per worker. We chose unary calls instead:

- **Any language, no gRPC stack.** A worker is an HTTP client that posts JSON. That is a
  few hundred lines in Python with `httpx`, and the same in Ruby, PHP or a shell script.
- **Works through every proxy.** Corporate proxies, load balancers and serverless gateways
  handle HTTP/1.1 requests; many break long-lived HTTP/2 streams.
- **Easy to debug.** Every interaction is one request you can replay with `curl`.
- **Reconnection is free.** There is no stream state to rebuild; the next request just works.

Long-polling keeps latency low: an idle worker has one open `Poll`, and the server answers
it as soon as work is ready. The cost is one extra round trip per assignment compared to a
push stream, which is small next to the work itself. A streaming fast path can be added
later without changing the semantics, because leases, acks and fencing don't depend on
the transport.

### Why Connect

Connect gives us gRPC, gRPC-Web and plain HTTP/JSON from **one** protobuf definition and
one Go handler. We keep a typed, versioned contract with generated code (and `buf lint` /
`buf breaking` checks) without forcing that toolchain on worker authors.

### Why pull-based scheduling

The worker asks for work when it has free slots, so it never gets more than it can run (no
separate flow-control mechanism), and the scheduler only evaluates one worker at a time. A
push scheduler can be added later behind the same filter interface.

### Why the control plane decides retries

If workers decided retries, each language's worker would have to implement backoff,
exception matching, guarantees and compensation the same way, and a worker crash would
lose that decision. With workers that only report facts, all policy lives in one place,
is recorded in history, and behaves the same for every worker implementation.

### Why replay runs on the worker

The workflow body is user code in the user's language, so only a worker can execute it.
Sending the full history with each step means **no worker keeps state between steps**:
any worker can take any step, and losing a worker loses nothing.

## Writing a worker in another language

A minimal worker that runs **tasks** only needs to:

1. `Register` with a join token; keep the credential.
2. Run a heartbeat loop with free slots and active lease ids; cancel the leases it is told
   to cancel; drain when asked.
3. `Poll` with `maxItems` = free slots.
4. For each task: `AckTask`, then run the function, then `CompleteTask` or `FailTask`.
   Encode results as `{"encoding": "json/plain", "data": base64(json)}`.
5. Exit when any call returns `unauthenticated`.

To also run **workflow bodies**, implement deterministic replay: assign refs in call order,
resolve calls from history, emit commands for the rest, and detect non-determinism. The
reference implementation is
[`sdk/src/flyke/_internal/replay.py`](../sdk/src/flyke/_internal/replay.py), under 400 lines.

Declare the runtime you support in `runtimes` (for example `go/1.26`) so the scheduler only
sends you work you can run.

Implementations to read:

- [`workers/python/src/flyke_worker/protocol.py`](../workers/python/src/flyke_worker/protocol.py):
  the full Python client on Connect JSON.
- [`flyke/server/server_test.go`](../flyke/server/server_test.go): a Go test acting as a
  worker through the generated gRPC client.

## Compatibility rules

- Within `flyke.worker.v1`, changes are **additive only**: new fields, RPCs and enum values.
  Existing field numbers, names and types never change. `buf breaking` enforces this.
- Workers must ignore fields and enum values they don't know.
- A worker sends `protocol_version: "v1"` at registration; the server rejects versions it
  does not speak with `failed_precondition`.
- An incompatible change means a new package, `flyke.worker.v2`, served next to `v1` for a
  transition period.

Planned, not built yet: version negotiation beyond the registration check, a streaming fast
path, and a black-box conformance suite that certifies third-party workers.
