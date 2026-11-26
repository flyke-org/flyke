"""flyke.secrets: resolved through the worker inside tasks, refused in the body."""

from __future__ import annotations

import asyncio
from datetime import UTC, datetime

import pytest

from flyke import ContextError, SecretNotFound, secrets, task, workflow
from flyke.runtime import HistoryEvent, RunContext, definition_of, execute_task, replay_step


@workflow()
class UsesSecrets:
    @task()
    async def connect(self) -> dict:
        return {
            "user": secrets.get("username"),
            "password": secrets["password"],
            "region": secrets.get("region", "eu-west-1"),
            "has_token": "token" in secrets,
        }

    @task()
    async def missing(self) -> str:
        return secrets["nope"]

    async def run(self) -> dict:
        return await self.connect()


@workflow()
class ReadsInBody:
    async def run(self) -> str:
        return secrets.get("password")


STORE = {"username": ("admin", False), "password": ("s3cret", True)}


def run_task(name: str, resolver):
    return asyncio.run(
        execute_task(
            definition_of(UsesSecrets),
            {},
            name,
            [],
            RunContext(task=name),
            lambda *a: None,
            secrets=resolver,
        )
    )


def test_task_reads_secrets_through_the_resolver_and_caches_them():
    calls = []

    def resolver(key):
        calls.append(key)
        return STORE.get(key)

    out = run_task("connect", resolver)
    assert out == {"user": "admin", "password": "s3cret", "region": "eu-west-1", "has_token": False}
    assert sorted(calls) == ["password", "region", "token", "username"]


def test_missing_secret_raises_secret_not_found():
    with pytest.raises(SecretNotFound) as e:
        run_task("missing", STORE.get)
    assert isinstance(e.value, KeyError) and "nope" in str(e.value)


def test_worker_without_resolver_is_an_error():
    with pytest.raises(ContextError):
        run_task("connect", None)


def test_workflow_body_cannot_read_secrets():
    started = HistoryEvent(id=1, type="RunStarted", time=datetime(2026, 1, 1, tzinfo=UTC))
    result = replay_step(definition_of(ReadsInBody), {}, [started], RunContext(), lambda *a: None)
    [fail] = result.commands
    assert fail.kind == "FailRun" and fail.failure.type.endswith("ContextError")
    assert "only allowed inside a @task" in fail.failure.message


def test_outside_flyke_falls_back_to_environment(monkeypatch):
    monkeypatch.setenv("FLYKE_TEST_SECRET", "from-env")
    assert secrets.get("FLYKE_TEST_SECRET") == "from-env"
    assert secrets.get("FLYKE_TEST_UNSET", None) is None
    with pytest.raises(SecretNotFound):
        secrets["FLYKE_TEST_UNSET"]
