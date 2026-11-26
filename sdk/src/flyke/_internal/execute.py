"""Execution of a single task attempt (or compensation)."""

from __future__ import annotations

import inspect
from collections.abc import Sequence
from typing import Any

from . import context
from .definition import WorkflowDef
from .errors import ContextError
from .logger import Logger, LogSink
from .secrets import SecretCache, SecretResolver


class TaskRuntime:
    kind = "task"

    def __init__(
        self, ctx: context.RunContext, log_sink: LogSink, secrets: SecretResolver | None = None
    ):
        self.ctx = ctx
        self.log = Logger(log_sink)
        self.secrets = SecretCache(secrets)


async def execute_task(
    defn: WorkflowDef,
    inputs: dict[str, Any],
    name: str,
    args: Sequence[Any],
    ctx: context.RunContext,
    log_sink: LogSink,
    *,
    compensation: bool = False,
    compensated_result: Any = None,
    secrets: SecretResolver | None = None,
) -> Any:
    """Run one attempt of a task and return its (JSON-able) result.

    ``secrets`` resolves keys read with ``flyke.secrets`` (synchronously; it
    is called from the task's thread). Exceptions propagate unchanged; the
    worker reports them as failures.
    """
    instance = defn.instantiate(inputs)
    if compensation:
        comp = defn.compensations.get(name)
        if comp is None:
            raise ContextError(f"workflow {defn.name} has no compensation {name!r}")
        fn, call_args = comp.fn, [*args, compensated_result]
    else:
        task = defn.tasks.get(name)
        if task is None:
            raise ContextError(f"workflow {defn.name} has no task {name!r}")
        fn, call_args = task.fn, list(args)

    token = context.activate(TaskRuntime(ctx, log_sink, secrets))
    try:
        result = fn(instance, *call_args)
        if inspect.isawaitable(result):
            result = await result
        return result
    finally:
        context.deactivate(token)
