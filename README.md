<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="branding/logo/flyke-logo-white-text.svg">
  <img src="branding/logo/flyke-logo.svg" alt="Flyke" width="340">
</picture>

### Durable workflows in plain Object Oriented Python.

Retries, sagas, waits for humans and replay after crashes, without a DAG or YAML.

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev)
[![Python](https://img.shields.io/badge/Python-3.11+-3776AB?style=flat-square&logo=python&logoColor=white)](https://www.python.org)
[![React](https://img.shields.io/badge/Dashboard-React_+_Vite-61DAFB?style=flat-square&logo=react&logoColor=black)](ui/)
[![SQLite](https://img.shields.io/badge/Storage-SQLite_built_in-003B57?style=flat-square&logo=sqlite&logoColor=white)](flyke/internal/adapter/store/sqlite)
[![Protocol](https://img.shields.io/badge/Protocol-Connect_/_gRPC-244c5a?style=flat-square)](docs/protocol.md)
[![License](https://img.shields.io/badge/License-ELv2_·_SDK_Apache--2.0-8A2BE2?style=flat-square)](LICENSING.md)
[![PRs welcome](https://img.shields.io/badge/PRs-welcome-brightgreen?style=flat-square)](CONTRIBUTING.md)

</div>

## Install

**CLI** (Linux / macOS), from the [latest release](https://github.com/flyke-org/flyke/releases/latest):

```sh
curl -fsSL "https://github.com/flyke-org/flyke/releases/latest/download/flyke_$(curl -fsSL https://api.github.com/repos/flyke-org/flyke/releases/latest | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')_$(uname -s | tr A-Z a-z)_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz" | tar -xz flyke
sudo mv flyke /usr/local/bin/
```

**Python SDK and worker**:

```sh
pip install flyke flyke-worker
```

**Docker images**: `ghcr.io/flyke-org/flyke-server` and `ghcr.io/flyke-org/flyke-worker`.

Releases are automated; see [release/README.md](release/README.md).
