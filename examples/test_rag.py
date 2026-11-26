"""Tests for the RAG example (rag.py).

They need no control plane or worker: `Sim` plays both, replaying the workflow
body against a growing history and running each scheduled task once.

    uv run pytest examples/test_rag.py
"""

from __future__ import annotations

import asyncio
import os
from datetime import UTC, datetime, timedelta
from typing import Any

import pytest

from flyke.runtime import HistoryEvent, RunContext, build_manifest, execute_task, load, replay_step

HERE = os.path.dirname(os.path.abspath(__file__))
DOCUMENT = "the quick brown fox jumps"
T0 = datetime(2026, 1, 1, tzinfo=UTC)


@pytest.fixture(scope="module")
def rag():
    return load(HERE, "rag.py:RAG")


class Sim:
    """A minimal in-memory control plane + worker for one run."""

    def __init__(self, defn, inputs: dict[str, Any], secrets: dict[str, str] | None = None):
        self.defn, self.inputs, self.secrets = defn, inputs, secrets or {}
        self.history: list[HistoryEvent] = []
        self.scheduled: list[Any] = []  # every ScheduleTask command, in order
        self.steps: list[list[str]] = []  # task names scheduled by each replay step
        self.logs: list[tuple] = []
        self.outcome: Any = None
        self._add("RunStarted")

    def _add(self, typ: str, **kw: Any) -> None:
        self.history.append(
            HistoryEvent(
                id=len(self.history) + 1,
                type=typ,
                time=T0 + timedelta(seconds=len(self.history)),
                **kw,
            )
        )

    def _sink(self, level, msg, fields):
        self.logs.append((level, msg, fields))

    def _resolve(self, key: str):
        return (self.secrets[key], True) if key in self.secrets else None

    def run(self, max_steps: int = 20) -> Sim:
        for _ in range(max_steps):
            res = replay_step(
                self.defn, self.inputs, list(self.history), RunContext(run_id="r1"), self._sink
            )
            assert res.failure is None, res.failure
            batch = [c for c in res.commands if c.kind == "ScheduleTask"]
            for c in res.commands:
                if c.kind in ("CompleteRun", "FailRun"):
                    self.outcome = c
            if self.outcome is not None:
                return self
            assert batch, "the workflow neither finished nor scheduled a task"
            self.steps.append([c.name for c in batch])
            for c in batch:
                self.scheduled.append(c)
                self._add("TaskScheduled", ref=c.ref, name=c.name, payload=c.payload)
            for c in batch:
                ctx = RunContext(run_id="r1", task=c.name, idempotency_key=c.idempotency_key)
                result = asyncio.run(
                    execute_task(
                        self.defn,
                        self.inputs,
                        c.name,
                        c.payload,
                        ctx,
                        self._sink,
                        secrets=self._resolve,
                    )
                )
                self._add("TaskCompleted", ref=c.ref, name=c.name, payload=result)
        raise AssertionError("workflow did not settle")

    def result_of(self, task: str) -> Any:
        return next(e.payload for e in self.history if e.type == "TaskCompleted" and e.name == task)

    def logged(self, msg: str) -> list[dict[str, Any]]:
        return [fields for _, m, fields in self.logs if m == msg]


def run_task(
    defn,
    inputs: dict[str, Any],
    name: str,
    args: list,
    secrets: dict[str, str] | None = None,
    key: str = "",
):
    """Run one task on its own; returns (result, logs)."""
    logs: list[tuple] = []
    resolve = lambda k: ((secrets or {})[k], True) if k in (secrets or {}) else None  # noqa: E731
    ctx = RunContext(task=name, idempotency_key=key)
    result = asyncio.run(
        execute_task(defn, inputs, name, args, ctx, lambda *a: logs.append(a), secrets=resolve)
    )
    return result, logs


# ---------------------------------------------------------------- definition


def test_manifest_describes_inputs_and_tasks(rag):
    m = build_manifest(rag, "rag.py:RAG")
    assert m["name"] == "RAG"
    assert m["description"] == "A RAG workflow"
    schema = m["inputs_schema"]
    assert schema["required"] == ["document"]
    assert schema["properties"]["chunking_strategy"]["default"] == "simple"
    assert schema["properties"]["chunk_size"] == {"type": "integer", "default": 2}
    assert set(m["tasks"]) == {
        "load_document",
        "chunk_document",
        "another_chunk_document_parallel",
        "embed_document",
    }


def test_task_guarantees_and_retry_policy(rag):
    tasks = build_manifest(rag, "rag.py:RAG")["tasks"]
    retry = tasks["load_document"]["retry"]
    assert (retry["attempts"], retry["backoff"], retry["delay_ms"]) == (5, "exp", 200)
    assert tasks["load_document"]["guarantee"] == "at_least_once"
    assert tasks["chunk_document"]["guarantee"] == "at_least_once"
    assert tasks["embed_document"]["guarantee"] == "effectively_once"
    assert tasks["embed_document"]["idempotency_key"] == "welcome-email-document-{document}"


# --------------------------------------------------------------------- tasks


def test_load_document_returns_the_input_document(rag):
    result, logs = run_task(rag, {"document": DOCUMENT}, "load_document", [])
    assert result == DOCUMENT
    assert ("info", "document loaded", {"document": DOCUMENT}) in logs


def test_load_document_falls_back_to_anonymous_without_secrets(rag):
    _, logs = run_task(rag, {"document": DOCUMENT}, "load_document", [])
    assert ("info", "connecting", {"username": "anonymous", "authenticated": False}) in logs


def test_load_document_uses_secrets_without_logging_the_password(rag):
    creds = {"infoblox_username": "svc-rag", "infoblox_password": "s3cret"}
    _, logs = run_task(rag, {"document": DOCUMENT}, "load_document", [], secrets=creds)
    assert ("info", "connecting", {"username": "svc-rag", "authenticated": True}) in logs
    assert "s3cret" not in repr(logs)


@pytest.mark.parametrize("task", ["chunk_document", "another_chunk_document_parallel"])
@pytest.mark.parametrize(
    "document, size, expected",
    [
        (DOCUMENT, 2, ["the", "quick"]),
        (DOCUMENT, 1, ["the"]),
        (
            DOCUMENT,
            99,
            ["the", "quick", "brown", "fox", "jumps"],
        ),  # fewer words than the chunk size
        ("  spaced \n out\ttext ", 3, ["spaced", "out", "text"]),  # any whitespace separates words
        ("", 2, []),
        (DOCUMENT, 0, []),
    ],
)
def test_chunking_keeps_the_first_words(rag, task, document, size, expected):
    result, _ = run_task(rag, {"document": document}, task, [document, size])
    assert result == expected


def test_embed_document_logs_every_chunk_with_the_idempotency_key(rag):
    result, logs = run_task(
        rag, {"document": DOCUMENT}, "embed_document", [["the", "quick"]], key="k-1"
    )
    assert result is None
    assert logs == [
        ("info", "chunk embedded", {"chunk": "the", "key": "k-1"}),
        ("info", "chunk embedded", {"chunk": "quick", "key": "k-1"}),
    ]


def test_embed_document_with_no_chunks_does_nothing(rag):
    result, logs = run_task(rag, {"document": ""}, "embed_document", [[]])
    assert result is None and logs == []


# ------------------------------------------------------------------ workflow


def test_run_schedules_the_tasks_one_after_the_other(rag):
    sim = Sim(rag, {"document": DOCUMENT}).run()
    assert sim.outcome.kind == "CompleteRun"
    assert sim.steps == [
        ["load_document"],
        ["another_chunk_document_parallel"],
        ["chunk_document"],
        ["embed_document"],
    ]


def test_run_passes_data_between_tasks(rag):
    sim = Sim(rag, {"document": DOCUMENT}).run()
    args = {c.name: c.payload for c in sim.scheduled}
    assert list(args["load_document"]) == []
    assert list(args["chunk_document"]) == [DOCUMENT, 2]  # default chunk_size
    assert list(args["another_chunk_document_parallel"]) == [DOCUMENT, 2]
    assert list(args["embed_document"]) == [["the", "quick"]]
    assert sim.result_of("chunk_document") == ["the", "quick"]


def test_run_honours_a_custom_chunk_size(rag):
    sim = Sim(rag, {"document": DOCUMENT, "chunk_size": 4}).run()
    assert sim.result_of("chunk_document") == ["the", "quick", "brown", "fox"]
    assert [f["chunk"] for f in sim.logged("chunk embedded")] == ["the", "quick", "brown", "fox"]


def test_run_embeds_with_an_idempotency_key_derived_from_the_document(rag):
    sim = Sim(rag, {"document": DOCUMENT}).run()
    embed = next(c for c in sim.scheduled if c.name == "embed_document")
    assert embed.idempotency_key == f"welcome-email-document-{DOCUMENT}"
    assert {f["key"] for f in sim.logged("chunk embedded")} == {embed.idempotency_key}


def test_run_reads_credentials_from_secrets(rag):
    sim = Sim(
        rag,
        {"document": DOCUMENT},
        secrets={"infoblox_username": "svc-rag", "infoblox_password": "s3cret"},
    ).run()
    assert sim.logged("connecting") == [{"username": "svc-rag", "authenticated": True}]


def test_replaying_a_finished_run_schedules_nothing_new(rag):
    sim = Sim(rag, {"document": DOCUMENT}).run()
    res = replay_step(rag, sim.inputs, list(sim.history), RunContext(run_id="r1"), lambda *a: None)
    assert res.failure is None
    assert [c.kind for c in res.commands] == ["CompleteRun"]


def test_run_requires_a_document(rag):
    with pytest.raises(TypeError, match="missing required input 'document'"):
        replay_step(
            rag,
            {},
            [HistoryEvent(id=1, type="RunStarted", time=T0)],
            RunContext(run_id="r1"),
            lambda *a: None,
        )


def test_run_rejects_unknown_inputs(rag):
    with pytest.raises(TypeError, match="unknown input"):
        Sim(rag, {"document": DOCUMENT, "chunks": 3}).run()
