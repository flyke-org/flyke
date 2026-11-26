"""Structured logger exposed to user code as ``self.log``."""

from __future__ import annotations

import logging
from collections.abc import Callable
from typing import Any

# sink(level, message, fields)
LogSink = Callable[[str, str, dict[str, Any]], None]


class Logger:
    """``self.log.info("image built", tag=tag)`` — message plus structured fields."""

    def __init__(self, sink: LogSink, enabled: Callable[[], bool] = lambda: True):
        self._sink = sink
        self._enabled = enabled

    def _emit(self, level: str, message: str, fields: dict[str, Any]) -> None:
        if self._enabled():
            self._sink(level, str(message), fields)

    def debug(self, message: str, **fields: Any) -> None:
        self._emit("debug", message, fields)

    def info(self, message: str, **fields: Any) -> None:
        self._emit("info", message, fields)

    def warning(self, message: str, **fields: Any) -> None:
        self._emit("warning", message, fields)

    warn = warning

    def error(self, message: str, **fields: Any) -> None:
        self._emit("error", message, fields)

    def exception(self, message: str, **fields: Any) -> None:
        import traceback

        fields = dict(fields, exc_info=traceback.format_exc())
        self._emit("error", message, fields)


_std = logging.getLogger("flyke.user")
_LEVELS = {
    "debug": logging.DEBUG,
    "info": logging.INFO,
    "warning": logging.WARNING,
    "error": logging.ERROR,
}


def stdlib_sink(level: str, message: str, fields: dict[str, Any]) -> None:
    """Used when user code runs outside Flyke (e.g. plain unit tests)."""
    extra = " ".join(f"{k}={v}" for k, v in fields.items())
    _std.log(_LEVELS.get(level, logging.INFO), f"{message} {extra}".rstrip())


fallback_logger = Logger(stdlib_sink)
