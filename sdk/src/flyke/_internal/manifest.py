"""Build the language-neutral manifest the control plane registers."""

from __future__ import annotations

import sys
from typing import Any

from .definition import TaskDef, WorkflowDef
from .graph import build_graph
from .options import Retry, resources_manifest, triggers_manifest
from .schema import inputs_schema

RUNTIME = f"python/{sys.version_info.major}.{sys.version_info.minor}"


def _task_manifest(t: TaskDef) -> dict[str, Any]:
    out: dict[str, Any] = {"guarantee": t.guarantee.value}
    if t.retry is not None:
        out["retry"] = t.retry.to_manifest()
    if t.on_unknown:
        out["on_unknown"] = t.on_unknown
    if t.timeout_ms:
        out["timeout_ms"] = t.timeout_ms
    if (res := resources_manifest(t.resources)) is not None:
        out["resources"] = res
    if t.pool:
        out["pool"] = t.pool
    if t.idempotency_key:
        out["idempotency_key"] = t.idempotency_key
    if t.compensation is not None:
        out["compensation"] = t.compensation.name
    params = list(t.signature.parameters)[1:]
    if params:
        out["params"] = params
    return out


def build_manifest(defn: WorkflowDef, entrypoint: str) -> dict[str, Any]:
    m: dict[str, Any] = {
        "name": defn.name,
        "description": defn.description,
        "entrypoint": entrypoint,
        "runtime": RUNTIME,
        "inputs_schema": inputs_schema(defn.fields),
        "tasks": {name: _task_manifest(t) for name, t in defn.tasks.items()},
    }
    comps = {}
    for t in defn.tasks.values():
        if t.compensation is not None:
            comps[t.compensation.name] = {
                "guarantee": "at_least_once",
                "retry": Retry(attempts=5).to_manifest(),
            }
    if comps:
        m["compensations"] = comps
    if defn.triggers:
        m["triggers"] = triggers_manifest(defn.triggers)
    if defn.concurrency is not None:
        m["concurrency"] = defn.concurrency.to_manifest()
    if (res := resources_manifest(defn.resources)) is not None:
        m["resources"] = res
    if defn.pool:
        m["pool"] = defn.pool
    if defn.timeout_ms:
        m["timeout_ms"] = defn.timeout_ms
    if defn.labels:
        m["labels"] = defn.labels
    if (graph := build_graph(defn)) is not None:
        m["graph"] = graph
    return m
