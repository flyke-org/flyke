"""Log capture and shipping. Users read logs from the control plane only."""

from __future__ import annotations

import asyncio
import io
import json
import logging
import sys
import threading
from collections import deque
from collections.abc import Callable
from datetime import UTC, datetime
from typing import Any

from .protocol import WorkerClient

log = logging.getLogger("flyke.worker")

MAX_BUFFER = 50_000
BATCH = 500


class LogShipper:
    """Buffers records and pushes them in batches; keeps them across outages."""

    def __init__(self, client: WorkerClient, interval: float = 0.2):
        self.client, self.interval = client, interval
        self._buf: deque[dict[str, Any]] = deque(maxlen=MAX_BUFFER)
        self._lock = threading.Lock()
        self._seq: dict[tuple, int] = {}
        self._wake = asyncio.Event()

    def sink_for(
        self, run_id: str, work_id: str, attempt: int, redact: Callable[[str], str] = lambda s: s
    ):
        """Return a sink(level, message, fields, stream="log") bound to one attempt."""

        def sink(level: str, message: str, fields: dict[str, Any], stream: str = "log") -> None:
            with self._lock:
                key = (work_id, attempt)
                seq = self._seq.get(key, 0) + 1
                self._seq[key] = seq
                try:
                    fields_json = json.dumps(fields, default=str) if fields else ""
                except Exception:  # noqa: BLE001
                    fields_json = json.dumps({k: repr(v) for k, v in fields.items()})
                if fields_json:
                    fields_json = redact(fields_json)
                self._buf.append(
                    {
                        "runId": run_id,
                        "workId": work_id,
                        "attempt": attempt,
                        "seq": seq,
                        "time": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
                        "level": level,
                        "message": redact(message),
                        "fieldsJson": fields_json,
                        "stream": stream,
                    }
                )

        return sink

    def forget(self, work_id: str, attempt: int) -> None:
        with self._lock:
            self._seq.pop((work_id, attempt), None)

    async def run(self, stop: asyncio.Event) -> None:
        while True:
            try:
                await asyncio.wait_for(stop.wait(), timeout=self.interval)
            except TimeoutError:
                pass
            await self.flush()
            if stop.is_set():
                await self.flush()
                return

    async def flush(self) -> None:
        while True:
            with self._lock:
                batch = [self._buf.popleft() for _ in range(min(BATCH, len(self._buf)))]
            if not batch:
                return
            try:
                await self.client.push_logs(batch)
            except Exception as e:  # noqa: BLE001 - keep logs, retry next tick
                with self._lock:
                    self._buf.extendleft(reversed(batch))
                log.debug("log push failed, will retry: %s", e)
                return


class Redactor:
    """Masks the sensitive secret values a task has read from its log lines.

    Values shorter than MIN_LEN are not masked: masking every "a" or "1" would
    garble logs while protecting nothing.
    """

    MIN_LEN = 4
    MASK = "********"

    def __init__(self) -> None:
        self._values: set[str] = set()
        self._lock = threading.Lock()

    def add(self, value: str) -> None:
        if len(value) >= self.MIN_LEN:
            with self._lock:
                self._values.add(value)
                # JSON-encoded form, as it appears inside structured fields.
                self._values.add(json.dumps(value)[1:-1])

    def __call__(self, text: str) -> str:
        with self._lock:
            values = sorted(self._values, key=len, reverse=True)
        for v in values:
            text = text.replace(v, self.MASK)
        return text


# ------------------------------------------------- stdout/stderr capture ----

_local = threading.local()


class _ThreadRoutedStream(io.TextIOBase):
    """Routes writes from task threads to their log sink, else to the real stream."""

    def __init__(self, real: Any, stream: str):
        self._real, self._stream = real, stream

    def write(self, s: str) -> int:
        sink: Callable[..., None] | None = getattr(_local, "sink", None)
        if sink is None:
            return self._real.write(s)
        buf = getattr(_local, "partial_" + self._stream, "") + s
        *lines, rest = buf.split("\n")
        setattr(_local, "partial_" + self._stream, rest)
        for line in lines:
            if line.strip():
                sink("info" if self._stream == "stdout" else "error", line, {}, self._stream)
        return len(s)

    def flush(self) -> None:
        sink = getattr(_local, "sink", None)
        for stream in ("stdout", "stderr"):
            rest = getattr(_local, "partial_" + stream, "")
            if sink and rest.strip():
                sink("info" if stream == "stdout" else "error", rest, {}, stream)
            setattr(_local, "partial_" + stream, "")
        self._real.flush()

    def isatty(self) -> bool:
        return False

    @property
    def encoding(self) -> str:  # type: ignore[override]
        return getattr(self._real, "encoding", "utf-8")


_installed = False


def install_capture() -> None:
    global _installed
    if not _installed:
        sys.stdout = _ThreadRoutedStream(sys.stdout, "stdout")  # type: ignore[assignment]
        sys.stderr = _ThreadRoutedStream(sys.stderr, "stderr")  # type: ignore[assignment]
        _installed = True


class capture_into:
    """Context manager: route this thread's stdout/stderr to sink."""

    def __init__(self, sink: Callable[..., None]):
        self.sink = sink

    def __enter__(self) -> None:
        _local.sink = self.sink

    def __exit__(self, *exc: Any) -> None:
        try:
            sys.stdout.flush()
        finally:
            _local.sink = None
