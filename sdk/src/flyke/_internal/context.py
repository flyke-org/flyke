"""The runtime currently executing user code (workflow replay or task)."""

from __future__ import annotations

import contextvars
from dataclasses import dataclass
from typing import Any, Protocol


@dataclass(frozen=True)
class RunContext:
    """Information about the current execution, available as ``self.ctx``."""

    run_id: str = ""
    workflow: str = ""
    namespace: str = "default"
    task: str = ""
    attempt: int = 1
    idempotency_key: str = ""
    worker: str = ""


class Runtime(Protocol):
    ctx: RunContext

    @property
    def log(self) -> Any: ...


_current: contextvars.ContextVar[Any | None] = contextvars.ContextVar("flyke_runtime", default=None)


def current() -> Any | None:
    return _current.get()


def activate(rt: Any) -> contextvars.Token:
    return _current.set(rt)


def deactivate(token: contextvars.Token) -> None:
    _current.reset(token)
