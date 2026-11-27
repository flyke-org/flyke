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

**CLI** (Linux / macOS):

```sh
curl -fsSL https://raw.githubusercontent.com/flyke-org/flyke/main/install.sh | sh
```

**Python SDK and worker**:

```sh
pip install flyke==0.1.0.dev0 flyke-worker==0.1.0.dev0
```

**Docker images**: `ghcr.io/flyke-org/flyke-server` and `ghcr.io/flyke-org/flyke-worker`.
