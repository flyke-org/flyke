# The `flyke` CLI

`flyke` is one Go binary that does two jobs:

1. **A client** for the control plane's REST API: start runs, stream logs, manage workers,
   pools, schedules, secrets and namespaces.
2. **A complete local environment**: it embeds the control plane and the dashboard, and
   starts a Python worker, so `flyke run` works with nothing installed or configured.

This page explains how the CLI is organised, how it finds a control plane, and what
happens when you type `flyke run`.

- [Command map](#command-map)
- [Finding a control plane](#finding-a-control-plane)
- [What `flyke run` does](#what-flyke-run-does)
- [Local mode](#local-mode)
- [`flyke deploy` and bundles](#flyke-deploy-and-bundles)
- [How the CLI talks to Python](#how-the-cli-talks-to-python)
- [Code layout](#code-layout)
- [Adding a command](#adding-a-command)

---

## Command map

```
flyke
├── run <file.py:Workflow> [--input=value…] [-f] [-d] [--bundle] [--root DIR]
├── server [--dev] [--data-dir] [--admin-token] [--addr] [--worker=false] [--slots]
├── deploy <file.py:Workflow>…  [--root DIR]
│
├── runs ls [--workflow] [--version] [--phase] [--limit]  -  runs get <run> [--history] [--json]
├── logs <run> [-f]
├── emit <event> [key=value…]
├── cancel <run>  -  resume <run>
├── retry <run> [--from <task|ref>] [--all] [--set k=v]… [--version same|<v>] [-f]
├── task resolve <run> <ref> --outcome=succeeded|failed|retry [--result JSON] [--message]
│
├── workflows ls  -  versions <wf> [v]  -  activate <wf> <v>  -  rollback <wf> [v]
├── schedules ls - get - create - update - pause - resume - trigger - rm - explain
├── secrets   ls - get - set <key> [value] [--sensitive] - rm          (alias: vars)
│
├── workers   ls [-l selector] - cordon - uncordon - drain - rm      (-A: all namespaces, admin)
├── pools     ls - create - rm - token <pool> [--ttl] [--max-uses] [--allow-label]   (--shared, admin)
├── namespaces ls - create - update - rm
├── tokens    ls - create - rm
└── whoami
```

Every command has `--help`. Global flags:

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--server` | `FLYKE_SERVER` | — | control plane URL |
| `--token` | `FLYKE_TOKEN` | — | admin token or namespace access token |
| `-n, --namespace` | `FLYKE_NAMESPACE` | `default` | namespace to act in |
| `--debug` | — | off | verbose output; with `flyke run`, also the local worker's own logs |

## Finding a control plane

Every command that needs a server resolves it the same way (`resolveServer` in
`cli/internal/cmd/root.go`):

```
--server / $FLYKE_SERVER set?  ──yes──▶  use it
        │ no
        ▼
~/.flyke/local.json exists and that server answers /healthz?  ──yes──▶  use it
        │ no
        ▼
flyke run    → start an ephemeral local control plane + worker (local mode)
anything else → error: "no control plane found: pass --server, or start one with flyke server --dev"
```

`~/.flyke/local.json` is written by every local control plane (`flyke run`, `flyke server
--dev`) with its URL and PID, and removed when it stops. That is how `flyke emit` in a second
terminal finds the run waiting in the first one.

## What `flyke run` does

```
flyke run examples/deploy.py:DeployService --service=api --env=prod -f
```

1. **Find Python.** `$FLYKE_PYTHON`, then `$VIRTUAL_ENV`, then a `.venv` next to the file or
   in any parent directory, then `python3` / `python`. The first one that can
   `import flyke, flyke_worker` wins.
2. **Read the workflow.** `python -m flyke manifest examples/deploy.py:DeployService` prints
   the workflow's manifest as JSON: name, entrypoint, inputs JSON schema, tasks and their
   options, triggers, and the diagram of `run()`. The CLI never parses Python itself.
3. **Build the inputs.** Flags after the target are workflow inputs. Their values are
   converted using the inputs schema: `--regions=eu,us` becomes a list, `--count=3` an
   integer, `--dry-run` / `--no-dry-run` a boolean, `--opts='{"a":1}'` an object.
4. **Find or start a control plane** (above).
5. **Ship the code.**
   - Local server and admin access: send `localRoot`, the code's directory. The worker
     reads it in place, so edits apply on the next run.
   - Otherwise (remote server, namespace token, or `--bundle`): pack the directory into a
     bundle, upload it, and send its digest.
6. **Register the workflow** (`PUT …/workflows/<name>`), which creates a new version if the
   code changed, then **start the run** (`POST …/workflows/<name>/runs`).
7. **Follow.** Wait for the run to end and set the exit code from its phase. With `-f`,
   stream its logs over SSE (`GET …/runs/<id>/logs?follow=true`) and print them with colours.
   With `-d`, print the run id and return at once (needs a running server).

With a dashboard, the CLI also prints the run's URL.

## Local mode

```
┌──────────────────────────── flyke (one process) ────────────────────────────┐
│                                                                             │
│   CLI command ──REST──▶ embedded control plane (flyke/server)               │
│                         - SQLite in memory (or --data-dir)                  │
│                         - dashboard from the embedded ui/ build             │
│                         - listens on 127.0.0.1:8742, or a free port         │
└──────────────────────────────────────▲──────────────────────────────────────┘
                                       │ flyke.worker.v1 over HTTP (loopback)
                      ┌────────────────┴────────────────┐
                      │  python -m flyke_worker join    │   subprocess, own process group
                      │    --ephemeral --slots 16       │   logs → temp file (or stderr with --debug)
                      └─────────────────────────────────┘
```

`cli/internal/local` boots the same `flyke/server` package the production binary uses, in
dev mode (fast resync, local workflow discovery for the dashboard). It creates a join token
for the `local` pool and starts the worker as a subprocess with that token, so the worker
**registers over the real protocol**, exactly like a remote one.

| | `flyke run` (nothing running) | `flyke server --dev` |
|---|---|---|
| Lifetime | the run | until Ctrl-C |
| State | in memory | in memory, or `--data-dir` |
| Worker | 1 local, 16 slots | 1 local (`--worker=false` for none, `--slots N`) |
| Dashboard | yes, while the run lasts | yes, http://127.0.0.1:8742 |

On exit, the CLI sends SIGTERM to the worker (which drains), kills its process group after
5 seconds if needed, shuts the server down and removes `~/.flyke/local.json`.

**Why local mode embeds the real control plane.** Developers get one command and no setup,
and what they test locally is the production code path: same scheduler, same store
interface, same protocol, same worker. Moving to a remote server changes a URL, not the
behaviour.

## `flyke deploy` and bundles

```bash
flyke deploy examples/deploy.py:DeployService examples/scheduled.py:Report
```

For each target, `deploy` reads the manifest, **always** uploads a bundle, and registers the
workflow. Registering enables its triggers: events, and schedules declared with
`on.schedule()` / `on.every()`, which `deploy` lists with their next run.

Bundles are built by `cli/internal/bundle`:

- a tar.gz of the code root (`--root`, default the file's directory);
- files sorted, timestamps and owners zeroed, so **the same code gives the same sha256**;
- `.git`, `.venv`, `venv`, `__pycache__`, `node_modules`, tool caches, any dot-directory and
  `*.pyc` skipped;
- at most 64 MiB.

The server stores bundles by digest; workers download each one once and cache it. Each
distinct bundle registered for a workflow is a **version**: `flyke workflows versions`,
`activate` and `rollback` manage which one new runs use, and `flyke retry --version same`
retries with the code the run started with.

## How the CLI talks to Python

The CLI is Go, the workflows are Python. They meet in three places, all through
subprocesses that print JSON:

| Command | Used by | Output |
|---|---|---|
| `python -m flyke manifest <file:Class> [--root]` | `run`, `deploy` | the workflow's manifest |
| `python -m flyke discover <root>` | the dev server's dashboard | every workflow under a directory, with import errors |
| `python -m flyke_worker join …` | local mode | a worker process |

Keeping the Python side behind these commands means the CLI has no Python embedding and no
knowledge of the SDK's internals. An SDK for another language would provide the same
`manifest` command.

## Code layout

```
cli/
├── cmd/flyke/main.go         entry point: os.Exit(cmd.Execute())
└── internal/
    ├── cmd/                  cobra commands
    │   ├── root.go           global flags, resolveServer, exit codes
    │   ├── run.go            flyke run: argument parsing, input coercion, follow
    │   ├── commands.go       server, deploy, runs, logs, emit, cancel, resume, retry, task, workers, pools, workflows
    │   ├── schedules.go      flyke schedules
    │   ├── secrets.go        flyke secrets
    │   ├── namespaces.go     flyke namespaces, tokens, whoami
    │   └── output.go         colours, tables, log formatting
    ├── client/               typed REST client (api/rest/v1 types), SSE log follower
    ├── local/                local mode: embedded server, worker subprocess, Python discovery
    └── bundle/               deterministic tar.gz packing
```

The CLI depends on `api/` (REST types), `flyke/server` (for local mode) and `ui/` (the
embedded dashboard). It never imports the control plane's internals: in client mode it only
uses the REST API, like the dashboard does.

## Adding a command

1. Write a `newXxxCmd() *cobra.Command` in the matching file under `cli/internal/cmd/`.
2. Get a client with `mustClient(cmd.Context())`, so `--server`, `--token`, `--namespace`
   and the local server lookup all work.
3. If you need a new endpoint, add its types to `api/rest/v1/types.go` and a method to
   `cli/internal/client/client.go`.
4. Print tables with the helpers in `output.go`, and support `--json` for anything a script
   might read.
5. Register it in `root.AddCommand(...)` in `root.go`, and document it in this page and in
   the README if users will need it.
