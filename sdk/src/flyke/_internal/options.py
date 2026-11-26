"""Declarative options for workflows and tasks. Each knows its manifest form."""

from __future__ import annotations

from collections.abc import Iterable
from dataclasses import dataclass, field
from enum import Enum
from typing import Any, Literal

from .units import Duration, duration_ms, size_bytes


class Guarantee(str, Enum):  # noqa: UP042 - StrEnum would change str()/format output
    AT_LEAST_ONCE = "at_least_once"
    AT_MOST_ONCE = "at_most_once"
    EFFECTIVELY_ONCE = "effectively_once"


AT_LEAST_ONCE = Guarantee.AT_LEAST_ONCE
AT_MOST_ONCE = Guarantee.AT_MOST_ONCE
EFFECTIVELY_ONCE = Guarantee.EFFECTIVELY_ONCE

OnUnknown = Literal["retry", "pause", "fail"]


def qualified_name(t: type) -> str:
    return f"{t.__module__}.{t.__qualname__}"


@dataclass(frozen=True)
class Retry:
    """Retry policy. ``attempts`` counts the first try (1 = never retry)."""

    attempts: int = 3
    backoff: Literal["exp", "linear", "fixed"] = "exp"
    delay: Duration = "1s"
    max_delay: Duration = "30s"
    on: tuple[type[BaseException], ...] = ()

    def __post_init__(self) -> None:
        if self.attempts < 1:
            raise ValueError("Retry.attempts must be >= 1")
        if self.backoff not in ("exp", "linear", "fixed"):
            raise ValueError("Retry.backoff must be 'exp', 'linear' or 'fixed'")
        duration_ms(self.delay)
        duration_ms(self.max_delay)
        if isinstance(self.on, type):
            object.__setattr__(self, "on", (self.on,))

    def to_manifest(self) -> dict[str, Any]:
        out: dict[str, Any] = {
            "attempts": self.attempts,
            "backoff": self.backoff,
            "delay_ms": duration_ms(self.delay),
            "max_delay_ms": duration_ms(self.max_delay),
        }
        if self.on:
            out["on"] = [qualified_name(t) for t in self.on]
        return out


@dataclass(frozen=True)
class Concurrency:
    """Limit concurrent runs sharing a key rendered from workflow inputs."""

    key: str
    limit: int = 1
    on_conflict: Literal["queue", "skip", "cancel_running"] = "queue"

    def to_manifest(self) -> dict[str, Any]:
        return {"key": self.key, "limit": self.limit, "on_conflict": self.on_conflict}


@dataclass(frozen=True)
class Resources:
    """Resource requests used for placement (cpu cores, memory like '512Mi')."""

    cpu: float | None = None
    memory: str | int | None = None

    def to_manifest(self) -> dict[str, Any]:
        out: dict[str, Any] = {}
        if self.cpu:
            out["cpu"] = float(self.cpu)
        if self.memory:
            out["memory_bytes"] = size_bytes(self.memory)
        return out


def resources_manifest(value: Resources | str | None) -> dict[str, Any] | None:
    """A Resources object, or the name of a pool resource profile ("heavy")."""
    if value is None:
        return None
    if isinstance(value, str):
        return {"profile": value}
    return value.to_manifest()


@dataclass(frozen=True)
class Trigger:
    type: str
    event: str = ""
    where: dict[str, Any] = field(default_factory=dict)
    cron: str = ""
    tz: str = ""
    inputs: dict[str, Any] = field(default_factory=dict)
    name: str = ""
    missed: str = ""

    def to_manifest(self) -> dict[str, Any]:
        out: dict[str, Any] = {"type": self.type}
        for k in ("event", "cron", "tz", "name", "missed"):
            if getattr(self, k):
                out[k] = getattr(self, k)
        if self.where:
            out["where"] = dict(self.where)
        if self.inputs:
            out["inputs"] = dict(self.inputs)
        return out


class on:  # noqa: N801 - reads as `on.event(...)`
    """Trigger factories."""

    @staticmethod
    def event(
        name: str, where: dict[str, Any] | None = None, inputs: dict[str, Any] | None = None
    ) -> Trigger:
        """Start a run when an event is emitted. ``inputs`` may use "{event.field}" templates;
        by default event fields that match workflow inputs are passed through."""
        return Trigger(type="event", event=name, where=where or {}, inputs=inputs or {})

    @staticmethod
    def schedule(
        cron: str,
        tz: str = "",
        inputs: dict[str, Any] | None = None,
        name: str = "",
        missed: Literal["", "run_once", "skip"] = "",
    ) -> Trigger:
        """Start a run on a cron schedule ("0 3 * * *", "@hourly").

        ``tz`` is an IANA time zone (default UTC). ``inputs`` may use the
        "{schedule.date}", "{schedule.time}" and "{schedule.name}" templates.
        ``name`` names the schedule (default: derived from the workflow name).
        ``missed`` says what to do with a tick missed while the control plane
        was down: "run_once" (default) or "skip".
        """
        if missed not in ("", "run_once", "skip"):
            raise ValueError("on.schedule: missed must be 'run_once' or 'skip'")
        return Trigger(
            type="schedule", cron=cron, tz=tz, inputs=inputs or {}, name=name, missed=missed
        )

    @staticmethod
    def every(
        interval: Duration,
        inputs: dict[str, Any] | None = None,
        name: str = "",
        missed: Literal["", "run_once", "skip"] = "",
    ) -> Trigger:
        """Start a run at a fixed interval: ``on.every("15m")`` (minimum 10s)."""
        ms = duration_ms(interval)
        if ms < 10_000 or ms % 1000:
            raise ValueError("on.every: the interval must be whole seconds, at least 10s")
        return on.schedule(f"@every {ms // 1000}s", inputs=inputs, name=name, missed=missed)


def triggers_manifest(triggers: Iterable[Trigger]) -> list:
    return [t.to_manifest() for t in triggers]
