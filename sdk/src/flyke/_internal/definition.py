"""The ``@workflow`` and ``@task`` decorators and the definitions they build."""

from __future__ import annotations

import copy
import inspect
import typing
from collections.abc import Callable, Sequence
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Any, ClassVar

from . import context
from .errors import ContextError
from .logger import fallback_logger
from .options import Concurrency, Guarantee, OnUnknown, Resources, Retry, Trigger
from .units import Duration, duration_ms

_MISSING = object()
_RESERVED = {"log", "ctx", "run", "wait_for", "emit", "sleep", "now"}


# --------------------------------------------------------------- tasks ----


class TaskDef:
    """A ``@task`` method. Calling it inside ``run()`` schedules it durably."""

    def __init__(
        self,
        fn: Callable[..., Any],
        *,
        retry: Retry | None = None,
        guarantee: Guarantee = Guarantee.AT_LEAST_ONCE,
        timeout: Duration = None,
        resources: Resources | str | None = None,
        pool: str | None = None,
        idempotency_key: str | None = None,
        on_unknown: OnUnknown | None = None,
    ):
        if not callable(fn):
            raise TypeError("@task must decorate a method")
        self.fn = fn
        self.name = fn.__name__
        self.retry = retry
        self.guarantee = Guarantee(guarantee)
        self.timeout_ms = duration_ms(timeout)
        self.resources = resources
        self.pool = pool
        self.idempotency_key = idempotency_key
        self.on_unknown = on_unknown
        self.compensation: CompensationDef | None = None
        self.signature = inspect.signature(fn)
        if self.guarantee == Guarantee.AT_MOST_ONCE and on_unknown == "retry":
            raise ValueError(f"task {self.name}: AT_MOST_ONCE tasks cannot use on_unknown='retry'")
        self.__doc__ = fn.__doc__
        self.__wrapped__ = fn

    def compensate(self, fn: Callable[..., Any]) -> CompensationDef:
        """Register the undo of this task. Called with the task's args then its result."""
        self.compensation = CompensationDef(fn, owner=self)
        return self.compensation

    def bind_args(self, instance: Any, args: Sequence[Any], kwargs: dict[str, Any]) -> list[Any]:
        """Normalise a call into positional arguments (defaults applied)."""
        try:
            bound = self.signature.bind(instance, *args, **kwargs)
        except TypeError as e:
            raise TypeError(f"task {self.name}: {e}") from None
        bound.apply_defaults()
        return list(bound.args[1:]) + list(bound.kwargs.values())

    def arg_map(self, args: Sequence[Any]) -> dict[str, Any]:
        names = list(self.signature.parameters)[1:]
        return dict(zip(names, args, strict=False))

    def __set_name__(self, owner: type, name: str) -> None:
        self.name = name

    def __get__(self, instance: Any, owner: type | None = None) -> Any:
        if instance is None:
            return self
        return _BoundTask(self, instance)


class _BoundTask:
    __slots__ = ("task", "instance")

    def __init__(self, task: TaskDef, instance: Any):
        self.task, self.instance = task, instance

    def __call__(self, *args: Any, **kwargs: Any) -> Any:
        rt = context.current()
        if rt is not None and getattr(rt, "kind", "") == "workflow":
            return rt.schedule_task(self.task, self.instance, args, kwargs)
        if rt is not None and getattr(rt, "kind", "") == "task":
            raise ContextError(
                f"task {self.task.name} cannot be called from another task; call it from run()"
            )
        # Plain Python call (unit tests): just run the function.
        return self.task.fn(self.instance, *args, **kwargs)

    def __repr__(self) -> str:
        return f"<flyke task {self.task.name}>"


class CompensationDef:
    def __init__(self, fn: Callable[..., Any], owner: TaskDef):
        self.fn, self.owner = fn, owner
        self.name = fn.__name__

    def __set_name__(self, owner: type, name: str) -> None:
        self.name = name

    def __get__(self, instance: Any, owner: type | None = None) -> Any:
        if instance is None:
            return self
        return self.fn.__get__(instance, owner)


def task(
    fn: Callable[..., Any] | None = None,
    *,
    retry: Retry | None = None,
    guarantee: Guarantee = Guarantee.AT_LEAST_ONCE,
    timeout: Duration = None,
    resources: Resources | str | None = None,
    pool: str | None = None,
    idempotency_key: str | None = None,
    on_unknown: OnUnknown | None = None,
) -> Any:
    """Declare a durable unit of work. Use bare (``@task``) or with options."""

    def wrap(f: Callable[..., Any]) -> TaskDef:
        return TaskDef(
            f,
            retry=retry,
            guarantee=guarantee,
            timeout=timeout,
            resources=resources,
            pool=pool,
            idempotency_key=idempotency_key,
            on_unknown=on_unknown,
        )

    return wrap(fn) if fn is not None else wrap


# ------------------------------------------------------------ workflows ----


@dataclass
class Field:
    name: str
    type: Any
    default: Any = _MISSING

    @property
    def required(self) -> bool:
        return self.default is _MISSING


@dataclass
class WorkflowDef:
    cls: type
    name: str
    description: str
    fields: dict[str, Field]
    tasks: dict[str, TaskDef]
    compensations: dict[str, CompensationDef]
    triggers: tuple[Trigger, ...] = ()
    concurrency: Concurrency | None = None
    resources: Resources | str | None = None
    timeout_ms: int = 0
    labels: dict[str, str] = field(default_factory=dict)
    pool: str | None = None

    def instantiate(self, inputs: dict[str, Any]) -> Any:
        """Create an instance with inputs applied (no __init__ side effects)."""
        unknown = set(inputs) - set(self.fields)
        if unknown:
            raise TypeError(f"{self.name}: unknown input(s) {sorted(unknown)}")
        obj = self.cls.__new__(self.cls)
        for f in self.fields.values():
            if f.name in inputs:
                value = inputs[f.name]
            elif not f.required:
                value = copy.deepcopy(f.default)
            else:
                raise TypeError(f"{self.name}: missing required input {f.name!r}")
            object.__setattr__(obj, f.name, value)
        return obj


def _collect_fields(cls: type) -> dict[str, Field]:
    hints: dict[str, Any] = {}
    for klass in reversed(cls.__mro__):
        if klass is object:
            continue
        try:
            hints.update(typing.get_type_hints(klass, include_extras=False))
        except Exception:  # unresolved forward refs: fall back to raw annotations
            hints.update(getattr(klass, "__annotations__", {}))
    out: dict[str, Field] = {}
    for name, hint in hints.items():
        if (
            name.startswith("_")
            or name in _RESERVED
            or typing.get_origin(hint) is ClassVar
            or hint is ClassVar
        ):
            continue
        default = getattr(cls, name, _MISSING)
        if isinstance(default, (TaskDef, CompensationDef, property)):
            continue
        out[name] = Field(name=name, type=hint, default=default)
    return out


class WorkflowAPI:
    """Methods injected into every ``@workflow`` class."""

    @property
    def log(self) -> Any:
        rt = context.current()
        return rt.log if rt is not None else fallback_logger

    @property
    def ctx(self) -> context.RunContext:
        rt = context.current()
        return rt.ctx if rt is not None else context.RunContext()

    def _workflow_runtime(self, api: str) -> Any:
        rt = context.current()
        if rt is None or getattr(rt, "kind", "") != "workflow":
            raise ContextError(f"self.{api}() can only be used inside run()")
        return rt

    def wait_for(
        self, event: str, where: dict[str, Any] | None = None, timeout: Duration = None
    ) -> Any:
        """Durably wait for an event (e.g. a human approval). Raises WaitTimeout."""
        return self._workflow_runtime("wait_for").wait_for(event, where or {}, duration_ms(timeout))

    def emit(self, event: str, **payload: Any) -> Any:
        """Publish an event (exactly once per workflow execution)."""
        return self._workflow_runtime("emit").emit(event, payload)

    def sleep(self, duration: Duration) -> Any:
        """Durable sleep: survives worker and control-plane restarts."""
        return self._workflow_runtime("sleep").sleep(duration_ms(duration))

    def now(self) -> datetime:
        """Deterministic current time inside run(); wall clock elsewhere."""
        rt = context.current()
        if rt is not None and getattr(rt, "kind", "") == "workflow":
            return rt.now()
        return datetime.now(UTC)


def _make_init(defn_ref: list[WorkflowDef]) -> Callable[..., None]:
    def __init__(self: Any, **inputs: Any) -> None:
        defn = defn_ref[0]
        built = defn.instantiate(inputs)
        self.__dict__.update(built.__dict__)

    return __init__


def workflow(
    cls: type | None = None,
    *,
    name: str | None = None,
    triggers: Sequence[Trigger] = (),
    concurrency: Concurrency | None = None,
    resources: Resources | str | None = None,
    timeout: Duration = None,
    labels: dict[str, str] | None = None,
    pool: str | None = None,
) -> Any:
    """Declare a workflow class. Fields are inputs; ``async def run(self)`` is the body."""

    def wrap(c: type) -> type:
        run = getattr(c, "run", None)
        if run is None or not inspect.iscoroutinefunction(run):
            raise TypeError(f"workflow {c.__name__} must define `async def run(self)`")
        tasks: dict[str, TaskDef] = {}
        comps: dict[str, CompensationDef] = {}
        for klass in reversed(c.__mro__):
            for attr, value in vars(klass).items():
                if isinstance(value, TaskDef):
                    tasks[attr] = value
                elif isinstance(value, CompensationDef):
                    comps[attr] = value
        for attr in ("log", "ctx", "wait_for", "emit", "sleep", "now", "_workflow_runtime"):
            if attr not in vars(c):
                setattr(c, attr, WorkflowAPI.__dict__[attr])
        defn = WorkflowDef(
            cls=c,
            name=name or c.__name__,
            description=inspect.getdoc(c) or "",
            fields=_collect_fields(c),
            tasks=tasks,
            compensations=comps,
            triggers=tuple(triggers),
            concurrency=concurrency,
            resources=resources,
            timeout_ms=duration_ms(timeout),
            labels=dict(labels or {}),
            pool=pool,
        )
        if "__init__" not in vars(c):
            c.__init__ = _make_init([defn])  # type: ignore[misc]
        c.__flyke_workflow__ = defn  # type: ignore[attr-defined]
        return c

    return wrap(cls) if cls is not None else wrap


def definition_of(obj: Any) -> WorkflowDef:
    defn = getattr(obj, "__flyke_workflow__", None)
    if not isinstance(defn, WorkflowDef):
        raise TypeError(f"{obj!r} is not a @workflow class")
    return defn
