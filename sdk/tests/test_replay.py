"""Replay semantics, exercised with a tiny in-memory orchestrator."""

from __future__ import annotations

import asyncio
import os
from datetime import UTC, datetime, timedelta
from typing import Any

import pytest

from flyke import NonRetryableError, Retry, task, workflow
from flyke.runtime import (
    Failure,
    HistoryEvent,
    RunContext,
    build_manifest,
    definition_of,
    execute_task,
    load,
    replay_step,
)

EXAMPLES = os.path.join(os.path.dirname(__file__), "..", "..", "examples")


class Sim:
    """Minimal stand-in for the control plane: applies commands, runs tasks once."""

    def __init__(self, defn, inputs: dict[str, Any]):
        self.defn, self.inputs = defn, inputs
        self.t0 = datetime(2026, 1, 1, tzinfo=UTC)
        self.history: list[HistoryEvent] = []
        self.logs: list[tuple] = []
        self.pending: list[HistoryEvent] = []
        self.waits: dict[int, HistoryEvent] = {}
        self.emitted: list[tuple] = []
        self.outcome: Any = None
        self._add("RunStarted")

    def _add(self, typ: str, **kw: Any) -> HistoryEvent:
        ev = HistoryEvent(
            id=len(self.history) + 1,
            type=typ,
            time=self.t0 + timedelta(seconds=len(self.history)),
            **kw,
        )
        self.history.append(ev)
        return ev

    def sink(self, level, msg, fields):
        self.logs.append((level, msg, fields))

    def step(self):
        res = replay_step(
            self.defn, self.inputs, list(self.history), RunContext(run_id="r1"), self.sink
        )
        assert res.failure is None, res.failure
        for c in res.commands:
            if c.kind == "ScheduleTask":
                self.pending.append(
                    self._add("TaskScheduled", ref=c.ref, name=c.name, payload=c.payload)
                )
            elif c.kind == "WaitEvent":
                self.waits[c.ref] = self._add(
                    "WaitStarted", ref=c.ref, name=c.name, payload=c.where
                )
            elif c.kind == "EmitEvent":
                self._add("EventEmitted", ref=c.ref, name=c.name, payload=c.payload)
                self.emitted.append((c.name, c.payload))
            elif c.kind == "StartTimer":
                self._add("TimerStarted", ref=c.ref, name=c.name)
                self._add("TimerFired", ref=c.ref, name=c.name)
            elif c.kind in ("CompleteRun", "FailRun"):
                self.outcome = c
        return res

    def run_tasks(self):
        batch, self.pending = self.pending, []
        for ev in batch:
            try:
                result = asyncio.run(
                    execute_task(
                        self.defn,
                        self.inputs,
                        ev.name,
                        ev.payload,
                        RunContext(task=ev.name),
                        self.sink,
                    )
                )
                self._add("TaskCompleted", ref=ev.ref, name=ev.name, payload=result)
            except Exception as e:  # noqa: BLE001
                self._add("TaskFailed", ref=ev.ref, name=ev.name, failure=Failure.from_exception(e))

    def send(self, name: str, payload: dict[str, Any]):
        for ref, w in list(self.waits.items()):
            if w.name == name:
                self._add("EventReceived", ref=ref, name=name, payload=payload)
                del self.waits[ref]

    def drive(self, max_steps: int = 20):
        for _ in range(max_steps):
            self.step()
            if self.outcome is not None or (not self.pending and self.waits):
                return
            self.run_tasks()
        raise AssertionError("workflow did not settle")


def deploy():
    return load(EXAMPLES, "deploy.py:DeployService")


def test_manifest_shape():
    m = build_manifest(deploy(), "deploy.py:DeployService")
    assert m["name"] == "DeployService"
    props = m["inputs_schema"]["properties"]
    assert m["inputs_schema"]["required"] == ["service"]
    assert props["env"] == {"enum": ["staging", "prod"], "type": "string", "default": "staging"}
    assert props["regions"]["type"] == "array" and props["regions"]["default"] == [
        "eu-west-1",
        "us-east-1",
    ]
    assert m["tasks"]["build"]["retry"]["on"] == ["builtins.ConnectionError"]
    assert m["tasks"]["build"]["retry"]["delay_ms"] == 200
    assert m["tasks"]["migrate_db"] == {
        "guarantee": "at_most_once",
        "on_unknown": "pause",
        "timeout_ms": 600_000,
        "resources": {"profile": "heavy"},
        "params": ["image"],
    }
    assert m["tasks"]["provision"]["compensation"] == "deprovision"
    assert "deprovision" in m["compensations"]
    assert m["resources"] == {"cpu": 1.0, "memory_bytes": 512 * 1024**2}
    assert m["concurrency"] == {"key": "{service}:{env}", "limit": 1, "on_conflict": "queue"}
    assert m["triggers"] == [{"type": "event", "event": "git.pushed", "where": {"branch": "main"}}]
    assert m["timeout_ms"] == 45 * 60_000


def test_staging_deploy_completes_and_logs_once():
    sim = Sim(
        deploy(),
        {"service": "api", "env": "staging", "regions": ["eu-west-1", "us-east-1"], "flaky": False},
    )
    sim.drive()
    assert sim.outcome.kind == "CompleteRun"
    result = sim.outcome.payload
    assert result["image"].startswith("api:")
    assert result["endpoints"] == [
        "https://api.eu-west-1.example.com",
        "https://api.us-east-1.example.com",
    ]
    # The two provision calls were scheduled in the same step (asyncio.gather).
    provisions = [e for e in sim.history if e.type == "TaskScheduled" and e.name == "provision"]
    assert [e.ref for e in provisions] == [3, 4]
    assert sim.emitted == [("deploy.finished", {"service": "api", "image": result["image"]})]
    # finished_at comes from history time, not the wall clock.
    assert result["finished_at"].startswith("2026-01-01")


def test_prod_waits_for_approval_then_finishes():
    sim = Sim(deploy(), {"service": "api", "env": "prod"})
    sim.drive()
    assert sim.outcome is None and len(sim.waits) == 1
    waiting_logs = [entry for entry in sim.logs if entry[1] == "waiting for approval"]
    sim.send("deploy.approved", {"service": "api", "user": "alice"})
    sim.drive()
    assert sim.outcome.kind == "CompleteRun"
    assert [
        entry for entry in sim.logs if entry[1] == "waiting for approval"
    ] == waiting_logs  # not re-logged on replay
    assert ("info", "approved", {"by": "alice"}) in sim.logs


def test_idempotency_key_is_rendered_from_fields_and_args():
    sim = Sim(deploy(), {"service": "api", "env": "prod"})
    sim.step()
    sim.run_tasks()  # build
    sim.step()
    sim.run_tasks()  # migrate
    res = sim.step()
    image = sim.history[[e.name for e in sim.history].index("build") + 1].payload
    keys = [c.idempotency_key for c in res.commands]
    assert keys == [f"api-prod-{image}-eu-west-1", f"api-prod-{image}-us-east-1"]


def test_task_failure_fails_the_run_with_task_error():
    sim = Sim(deploy(), {"service": "broken"})
    sim.drive()
    assert sim.outcome.kind == "FailRun"
    f = sim.outcome.failure
    assert f.type == "flyke._internal.errors.TaskError"
    assert "migration 0042" in f.message


def test_nondeterminism_is_detected():
    defn = deploy()
    bogus = [
        HistoryEvent(1, "RunStarted", datetime.now(UTC)),
        HistoryEvent(2, "TaskScheduled", datetime.now(UTC), ref=1, name="something_else"),
    ]
    res = replay_step(defn, {"service": "api"}, bogus, RunContext(), lambda *a: None)
    assert res.failure is not None and res.failure.type.endswith("NonDeterminismError")
    assert res.commands == []


def test_wall_clock_sleep_in_run_is_rejected():
    @workflow
    class Sleepy:
        async def run(self) -> None:
            await asyncio.sleep(1)

    res = replay_step(
        definition_of(Sleepy),
        {},
        [HistoryEvent(1, "RunStarted", datetime.now(UTC))],
        RunContext(),
        lambda *a: None,
    )
    assert res.failure is not None and "self.sleep" in res.failure.message


def test_durable_sleep_and_plain_calls():
    @workflow
    class Napper:
        n: int = 2

        @task(retry=Retry(attempts=1))
        async def double(self, x: int) -> int:
            if x < 0:
                raise NonRetryableError("negative")
            return x * 2

        async def run(self) -> int:
            await self.sleep("1h")
            return await self.double(self.n)

    sim = Sim(definition_of(Napper), {"n": 21})
    sim.drive()
    assert sim.outcome.kind == "CompleteRun" and sim.outcome.payload == 42
    # Outside Flyke, tasks are plain async methods (easy unit testing).
    assert asyncio.run(Napper(n=1).double(5)) == 10
    with pytest.raises(NonRetryableError):
        asyncio.run(Napper().double(-1))
