"""Exceptions raised by or to user code."""

from __future__ import annotations

from typing import Any


class FlykeError(Exception):
    """Base class of Flyke errors."""


class NonRetryableError(Exception):
    """Raise from a task to fail it immediately, without retries."""


class TaskError(FlykeError):
    """Raised inside a workflow body when a task failed permanently."""

    def __init__(self, task: str, type: str, message: str, failure: Any | None = None):
        self.task, self.type, self.message, self.failure = task, type, message, failure
        short = type.rsplit(".", 1)[-1]
        super().__init__(
            f"task {task} failed: {short}: {message}" if message else f"task {task} failed: {short}"
        )


class WaitTimeout(FlykeError, TimeoutError):
    """Raised by wait_for() when the timeout elapses before a matching event."""


class NonDeterminismError(FlykeError):
    """The workflow body did something different from what its history recorded.

    Usually caused by changing code under a running workflow, or by using
    non-deterministic APIs (time, random, I/O) in run() instead of in tasks.
    """


class ContextError(FlykeError):
    """A workflow API was used outside of the context where it is valid."""
