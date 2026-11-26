# Contributing to Flyke

Thanks for wanting to help. Bug reports, docs fixes, examples and code are all welcome.
This page explains how to get a change merged with as little back-and-forth as possible.

- [Ways to contribute](#ways-to-contribute)
- [Before you start](#before-you-start)
- [Set up your environment](#set-up-your-environment)
- [Make a change](#make-a-change)
- [Tests](#tests)
- [Code style](#code-style)
- [Commit messages and pull requests](#commit-messages-and-pull-requests)
- [Where things live](#where-things-live)
- [Licensing of contributions](#licensing-of-contributions)

## Ways to contribute

| You want to… | Do this |
|---|---|
| Report a bug | Open an issue with the Flyke version (`flyke --version`), your OS, the workflow (or a minimal one that reproduces it), what you expected and what happened. `flyke logs <run>` output helps a lot. |
| Suggest a feature | Open an issue describing the problem first, then the solution you have in mind. |
| Fix a typo or a doc | Open a pull request directly. |
| Add an example | Add a file under `examples/` and a row to `examples/README.md`. |
| Write a worker in another language | Read [docs/protocol.md](docs/protocol.md) and open an issue so we can link to it. |
| Change the protocol, the store schema or the REST API | Open an issue **before** writing code. These are contracts other people depend on. |

> [!IMPORTANT]
> Please report **security issues privately** to the maintainers (GitHub *Security → Report a
> vulnerability*), not in a public issue.

## Before you start

- For anything bigger than a small fix, comment on an issue (or open one) first, so two
  people don't build the same thing and the approach is agreed before you spend time on it.
- Keep pull requests focused: one change per PR is much faster to review than five.

## Set up your environment

The full guide, with Docker Compose and without, is in
**[docs/development.md](docs/development.md)**. In short:

```bash
git clone https://github.com/flyke-org/flyke.git && cd flyke
make dev        # uv sync: SDK + worker in ./.venv (editable), installs pre-commit hooks
make build      # builds the dashboard and ./bin/flyke, ./bin/flyke-server
make test       # everything CI runs
```

You need Go 1.26+, Python 3.11+, [uv](https://docs.astral.sh/uv/), Node.js LTS, and
[buf](https://buf.build/docs/installation) only if you change the protocol.

## Make a change

1. Fork the repository and create a branch from `main`: `git switch -c fix/sleep-timer`.
2. Make the change, with tests (see below).
3. Run `make fmt test`.
4. If you changed behaviour users can see, update the docs in the same PR
   (`README.md`, `docs/`, the CLI `--help` text).
5. Open a pull request.

### Changing the protocol (`api/proto`)

The worker protocol is a public contract: workers in other languages implement it.

- Only **additive** changes inside `flyke.worker.v1`: new fields, new RPCs, new enum values.
  Never renumber, rename or change the type of an existing field.
- Run `make gen` and commit the generated code under `api/gen/`.
- Run `make lint` (`buf lint` + `buf breaking` against `main`).
- Update [docs/protocol.md](docs/protocol.md) and both sides: the server
  (`flyke/internal/adapter/workerapi`) and the Python worker (`workers/python`).

### Changing the store schema

Schema changes go in `flyke/internal/adapter/store/sqlite/schema.sql` and must migrate
existing databases. Add a test in `store_test.go` that opens an older schema (see
`testdata/schema_v0.sql`) and checks that it upgrades.

### Changing the dashboard

The UI uses the REST API only. Run `flyke server --dev` and `make ui-dev` side by side, and
check that `make test-ui` (typecheck) passes.

## Tests

| Command | What it runs |
|---|---|
| `make test` | all of the below |
| `make test-go` | `go vet` and `go test -race` in `api`, `flyke` and `cli` |
| `make test-py` | `pytest` for the SDK and the worker |
| `make test-ui` | TypeScript typecheck of the dashboard |

What we expect in a PR:

- **A bug fix comes with a test that fails without the fix.**
- Domain logic (`flyke/internal/domain`) and use cases (`flyke/internal/app`) have unit tests.
- Replay behaviour has a test in `sdk/tests/test_replay.py`.

## Code style

- **Go**: `gofmt`, `go vet`. Follow the dependency rule: `domain` imports nothing from the
  project, `app` depends on `domain` and `ports` only, adapters implement `ports`. The
  application layer never imports an adapter.
- **Python**: [ruff](https://docs.astral.sh/ruff/) lint and format, line length 100,
  Python 3.11 syntax. The pre-commit hook runs it on every commit; `make fmt` runs it on
  everything. The SDK (`sdk/`) has **no runtime dependencies**: keep it that way.
- **TypeScript**: strict mode, no type errors.
- Write comments that explain *why*, not *what*. Error messages say what went wrong and,
  when possible, what to do about it.

## Commit messages and pull requests

- Write commit subjects in the imperative, under ~72 characters: `Fix timer firing twice
  after a control-plane restart`.
- Explain in the PR description **what** changed and **why**, how you tested it, and link
  the issue (`Fixes #123`).
- Mark breaking changes clearly in the title, e.g. `[breaking]`.
- A maintainer reviews every PR. Expect questions; they are about the code, not about you.

## Where things live

```
api/            protocol (proto) and REST types: the contract
flyke/          control plane: domain → app → adapters; server/ is the composition root
cli/            the `flyke` CLI and local mode
sdk/            Python SDK: decorators, manifest, deterministic replayer
workers/python/ Python worker agent
ui/             React dashboard
docs/           user and design docs
```

Topic guides:

- [docs/development.md](docs/development.md): developing locally
- [docs/protocol.md](docs/protocol.md): the worker protocol and why it looks the way it does
- [docs/worker.md](docs/worker.md): how the Python worker runs your code
- [docs/cli.md](docs/cli.md): how the CLI is organised and how local mode works

## Licensing of contributions

Flyke is under the Elastic License 2.0, except `sdk/`, `workers/python/` and `api/`, which
are under Apache-2.0 (see [LICENSING.md](LICENSING.md)). By submitting a contribution, you
agree that it is licensed under the license of the directory it is in, and that you have the
right to submit it.
