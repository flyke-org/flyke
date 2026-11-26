"""Deterministic replay of a workflow body against its history.

A workflow step re-executes ``run()`` from the start on a private event loop.
Every durable call (task, timer, wait, emit) receives a sequence number
("ref") in call order. If history already holds the outcome for that ref the
awaitable resolves immediately; otherwise a command is recorded and the
awaitable stays pending. When the loop has nothing left to do, the step ends
and its new commands are returned to the control plane.
"""

from __future__ import annotations

import asyncio
import traceback
from collections.abc import Sequence
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Any

from . import context
from .codec import dumps, to_jsonable
from .definition import TaskDef, WorkflowDef
from .errors import NonDeterminismError, TaskError, WaitTimeout
from .logger import Logger, LogSink
from .options import qualified_name
from .units import duration_ms

# Event types (language-neutral names, mirroring the protocol enum).
RUN_STARTED = "RunStarted"
TASK_SCHEDULED = "TaskScheduled"
TASK_COMPLETED = "TaskCompleted"
TASK_FAILED = "TaskFailed"
TIMER_STARTED = "TimerStarted"
TIMER_FIRED = "TimerFired"
WAIT_STARTED = "WaitStarted"
EVENT_RECEIVED = "EventReceived"
WAIT_TIMED_OUT = "WaitTimedOut"
EVENT_EMITTED = "EventEmitted"

_SCHEDULING = {
    TASK_SCHEDULED: "task",
    TIMER_STARTED: "timer",
    WAIT_STARTED: "wait",
    EVENT_EMITTED: "emit",
}
_RESOLVING = {TASK_COMPLETED, TASK_FAILED, TIMER_FIRED, EVENT_RECEIVED, WAIT_TIMED_OUT}


@dataclass
class Failure:
    type: str
    message: str = ""
    mro: list[str] = field(default_factory=list)
    stack: str = ""
    non_retryable: bool = False

    @classmethod
    def from_exception(cls, exc: BaseException) -> Failure:
        from .errors import NonRetryableError

        mro = [qualified_name(t) for t in type(exc).__mro__ if t not in (object, BaseException)]
        if isinstance(exc, TaskError):
            message = str(exc)
        else:
            message = str(exc) or type(exc).__name__
        return cls(
            type=qualified_name(type(exc)),
            message=message,
            mro=mro,
            stack="".join(traceback.format_exception(type(exc), exc, exc.__traceback__)),
            non_retryable=isinstance(exc, NonRetryableError),
        )


@dataclass
class HistoryEvent:
    id: int
    type: str
    time: datetime
    ref: int = 0
    name: str = ""
    payload: Any = None
    failure: Failure | None = None


@dataclass
class Command:
    ref: int
    kind: str  # ScheduleTask | StartTimer | WaitEvent | EmitEvent | CompleteRun | FailRun
    name: str = ""
    payload: Any = None
    where: dict[str, Any] | None = None
    duration_ms: int = 0
    idempotency_key: str = ""
    failure: Failure | None = None


@dataclass
class StepResult:
    commands: list[Command]
    #: Set when the step itself failed (non-determinism, import error…).
    #: The run is paused instead of failed so the code can be fixed.
    failure: Failure | None = None


class Event:
    """An event received by ``wait_for``: attribute access to its payload."""

    def __init__(self, name: str, payload: dict[str, Any] | None):
        self.name = name
        self.payload = dict(payload or {})

    def __getattr__(self, item: str) -> Any:
        try:
            return self.__dict__["payload"][item]
        except KeyError:
            raise AttributeError(f"event {self.name!r} has no field {item!r}") from None

    def __getitem__(self, item: str) -> Any:
        return self.payload[item]

    def get(self, item: str, default: Any = None) -> Any:
        return self.payload.get(item, default)

    def __repr__(self) -> str:
        return f"Event({self.name!r}, {self.payload!r})"


class ReplayRuntime:
    kind = "workflow"

    def __init__(
        self,
        defn: WorkflowDef,
        inputs: dict[str, Any],
        history: Sequence[HistoryEvent],
        ctx: context.RunContext,
        log_sink: LogSink,
    ):
        self.defn, self.inputs, self.ctx = defn, inputs, ctx
        self.scheduled: dict[int, HistoryEvent] = {}
        self.resolved: dict[int, HistoryEvent] = {}
        self.clock = datetime.now(UTC)
        for ev in history:
            if ev.type == RUN_STARTED:
                self.clock = ev.time
            elif ev.type in _SCHEDULING:
                self.scheduled[ev.ref] = ev
            elif ev.type in _RESOLVING:
                self.resolved[ev.ref] = ev
        self.max_ref = max(self.scheduled, default=0)
        self.next_ref = 1
        self.commands: list[Command] = []
        self.nondeterminism: NonDeterminismError | None = None
        self.closed = False
        self.loop: asyncio.AbstractEventLoop | None = None
        self.log = Logger(log_sink, enabled=lambda: not self.replaying and not self.closed)

    @property
    def replaying(self) -> bool:
        """True while re-issuing calls already recorded in history."""
        return self.next_ref <= self.max_ref

    # ----------------------------------------------------------- calls ----

    def _take_ref(self, kind: str, name: str) -> tuple[int, bool]:
        if self.closed:
            raise asyncio.CancelledError()
        ref = self.next_ref
        self.next_ref += 1
        ev = self.scheduled.get(ref)
        if ev is None:
            return ref, False
        if _SCHEDULING[ev.type] != kind or ev.name != name:
            err = NonDeterminismError(
                f"at call #{ref} the history recorded {_SCHEDULING[ev.type]} {ev.name!r} "
                f"but the code now calls {kind} {name!r}. Code changed under a running workflow, "
                f"or run() uses non-deterministic logic (time, random, I/O) — move it into a @task."
            )
            self.nondeterminism = err
            raise err
        return ref, True

    def _future(self) -> asyncio.Future:
        assert self.loop is not None
        return self.loop.create_future()

    def _resolve(self, fut: asyncio.Future, ev: HistoryEvent) -> None:
        if ev.time > self.clock:
            self.clock = ev.time
        if ev.type == TASK_COMPLETED:
            fut.set_result(ev.payload)
        elif ev.type == TASK_FAILED:
            f = ev.failure or Failure(type="flyke.UnknownError")
            fut.set_exception(TaskError(ev.name, f.type, f.message, failure=f))
        elif ev.type == TIMER_FIRED:
            fut.set_result(None)
        elif ev.type == EVENT_RECEIVED:
            fut.set_result(Event(ev.name, ev.payload))
        elif ev.type == WAIT_TIMED_OUT:
            fut.set_exception(WaitTimeout(f"no {ev.name!r} event before the timeout"))

    def _durable(self, kind: str, name: str, command: Command) -> asyncio.Future:
        ref, known = self._take_ref(kind, name)
        fut = self._future()
        if known:
            if (ev := self.resolved.get(ref)) is not None:
                self._resolve(fut, ev)
        else:
            command.ref = ref
            self.commands.append(command)
        return fut

    def schedule_task(
        self, task: TaskDef, instance: Any, args: Sequence[Any], kwargs: dict[str, Any]
    ) -> asyncio.Future:
        positional = task.bind_args(instance, args, kwargs)
        payload = to_jsonable(positional)
        key = ""
        if task.idempotency_key:
            variables = {
                **{f: getattr(instance, f, None) for f in self.defn.fields},
                **task.arg_map(positional),
            }
            try:
                key = task.idempotency_key.format_map(variables)
            except KeyError as e:
                raise ValueError(
                    f"task {task.name}: idempotency_key references unknown field {e}"
                ) from None
        return self._durable(
            "task",
            task.name,
            Command(0, "ScheduleTask", task.name, payload=payload, idempotency_key=key),
        )

    def wait_for(self, event: str, where: dict[str, Any], timeout_ms: int) -> asyncio.Future:
        cmd = Command(0, "WaitEvent", event, where=to_jsonable(where), duration_ms=timeout_ms)
        return self._durable("wait", event, cmd)

    def sleep(self, ms: int) -> asyncio.Future:
        return self._durable("timer", "sleep", Command(0, "StartTimer", "sleep", duration_ms=ms))

    def emit(self, event: str, payload: dict[str, Any]) -> asyncio.Future:
        fut = self._durable(
            "emit", event, Command(0, "EmitEvent", event, payload=to_jsonable(payload))
        )
        if not fut.done():
            fut.set_result(None)  # fire-and-forget: published when the step commits
        return fut

    def now(self) -> datetime:
        return self.clock

    # ---------------------------------------------------------- driving ----

    def run(self) -> StepResult:
        loop = asyncio.new_event_loop()
        self.loop = loop
        token = context.activate(self)
        try:
            instance = self.defn.instantiate(self.inputs)
            main = loop.create_task(instance.run())
            loop.run_until_complete(self._settle(main))
            return self._result(main)
        except NonDeterminismError as e:
            return StepResult([], Failure.from_exception(e))
        finally:
            self._shutdown(loop)
            context.deactivate(token)

    async def _settle(self, main: asyncio.Task) -> None:
        loop = asyncio.get_running_loop()
        idle = 0
        while not main.done():
            await asyncio.sleep(0)
            if main.done():
                return
            if loop._ready:  # type: ignore[attr-defined]
                idle = 0
                continue
            if loop._scheduled:  # type: ignore[attr-defined]
                self.nondeterminism = NonDeterminismError(
                    "run() is waiting on a wall-clock timer (asyncio.sleep / asyncio.wait_for); "
                    "use `await self.sleep(...)` or task timeouts instead"
                )
                return
            idle += 1
            if idle >= 3:
                return  # everything is blocked on unresolved durable calls

    def _result(self, main: asyncio.Task) -> StepResult:
        if main.done() and not main.cancelled():
            main.exception()  # mark as retrieved in every branch
        if self.nondeterminism is not None:
            return StepResult([], Failure.from_exception(self.nondeterminism))
        cmds = list(self.commands)
        if not main.done():
            return StepResult(cmds)
        exc = main.exception()
        if exc is not None:
            if isinstance(exc, NonDeterminismError):
                return StepResult([], Failure.from_exception(exc))
            cmds.append(Command(self.next_ref, "FailRun", failure=Failure.from_exception(exc)))
            return StepResult(cmds)
        try:
            dumps(main.result())
            payload = to_jsonable(main.result())
        except TypeError as e:
            cmds.append(Command(self.next_ref, "FailRun", failure=Failure.from_exception(e)))
            return StepResult(cmds)
        cmds.append(Command(self.next_ref, "CompleteRun", payload=payload))
        return StepResult(cmds)

    def _shutdown(self, loop: asyncio.AbstractEventLoop) -> None:
        self.closed = True
        try:
            pending = [t for t in asyncio.all_tasks(loop) if not t.done()]
            for t in pending:
                t.cancel()
            if pending:

                async def drain() -> None:
                    for _ in range(100):
                        if all(t.done() for t in pending):
                            return
                        await asyncio.sleep(0)

                loop.run_until_complete(drain())
            for t in pending:
                t._log_destroy_pending = False  # type: ignore[attr-defined]
                if t.done() and not t.cancelled():
                    t.exception()  # mark retrieved
        finally:
            loop.close()


def replay_step(
    defn: WorkflowDef,
    inputs: dict[str, Any],
    history: Sequence[HistoryEvent],
    ctx: context.RunContext,
    log_sink: LogSink,
) -> StepResult:
    """Advance a workflow by one step. Pure function of (code, inputs, history)."""
    return ReplayRuntime(defn, inputs, history, ctx, log_sink).run()


__all__ = [
    "Command",
    "Event",
    "Failure",
    "HistoryEvent",
    "ReplayRuntime",
    "StepResult",
    "replay_step",
    "duration_ms",
]
