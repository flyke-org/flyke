# Licensing

Copyright © 2026 Abdesslem Amri and the Flyke contributors.

Flyke uses two licenses. The **control plane, CLI and dashboard** are under the
[Elastic License 2.0](LICENSE) (ELv2). The **pieces that end up inside your own code
or let you build on Flyke** (the Python SDK, the Python worker and the protocol
definition) are under the [Apache License 2.0](sdk/LICENSE).

| Path | License | File |
|---|---|---|
| `sdk/` (Python package `flyke`) | Apache-2.0 | [`sdk/LICENSE`](sdk/LICENSE) |
| `workers/python/` (Python package `flyke-worker`) | Apache-2.0 | [`workers/python/LICENSE`](workers/python/LICENSE) |
| `api/` (worker protocol and REST types) | Apache-2.0 | [`api/LICENSE`](api/LICENSE) |
| Everything else: `flyke/`, `cli/`, `ui/`, `benchmark/`, `docker/`, `docs/`, `examples/` | Elastic License 2.0 | [`LICENSE`](LICENSE) |

A file in a directory that has its own `LICENSE` is under that license. Every other
file is under the root `LICENSE`.

## What this means in practice

> [!NOTE]
> This is a plain-language summary, not legal advice. The license texts are what
> apply.

**You can**, for free:

- use Flyke in production, inside your company, for any number of users, workflows and workers;
- run it for your own products and services, including commercial ones, where
  Flyke orchestrates your own work;
- read, modify and fork the code, and redistribute it (with the license and
  notices kept);
- embed the SDK and the worker in your own software, under Apache-2.0.

**You cannot**, under the Elastic License 2.0:

- offer Flyke to third parties as a **hosted or managed service** that gives
  them access to a substantial part of its features (a "Flyke cloud");
- remove or hide the licensing, copyright or other notices.

### Examples

| Situation | Allowed? |
|---|---|
| Your team runs Flyke to deploy your services and onboard your employees | ✅ yes |
| Your SaaS product runs its billing and e-mail workflows on Flyke | ✅ yes |
| A consultancy installs and operates Flyke on a client's own infrastructure | ✅ yes |
| You write a Go worker that speaks `flyke.worker.v1` and publish it | ✅ yes (the protocol is Apache-2.0) |
| You sell "managed Flyke": customers sign up and run their own workflows on your Flyke servers | ❌ no |

If you want to do something the license does not allow, open an issue or contact
the maintainers for a commercial license.

## Why not an OSI open-source license for everything?

The Elastic License 2.0 is **source-available**, not an OSI-approved open-source
license. It was chosen because it has one restriction that matters here (no
competing hosted service) and is otherwise permissive, short, and already
understood by companies' legal teams (Elastic, Airbyte and others use it). The SDK,
the worker and the protocol stay Apache-2.0 so that nothing in your own codebase,
and no worker written for another language, is affected by that restriction.

## Contributions

By contributing, you agree that your contribution is licensed under the license of
the directory it is in. See [CONTRIBUTING.md](CONTRIBUTING.md).
