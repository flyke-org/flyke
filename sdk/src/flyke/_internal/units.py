"""Parsing of human-friendly durations ("200ms", "1h30m") and sizes ("512Mi")."""

from __future__ import annotations

import re
from datetime import timedelta

Duration = str | int | float | timedelta | None

_DURATION_PART = re.compile(r"(\d+(?:\.\d+)?)(ms|s|m|h|d)")
_UNITS_MS = {"ms": 1, "s": 1000, "m": 60_000, "h": 3_600_000, "d": 86_400_000}


def duration_ms(value: Duration) -> int:
    """Return a duration in milliseconds. Numbers are seconds."""
    if value is None:
        return 0
    if isinstance(value, timedelta):
        return int(value.total_seconds() * 1000)
    if isinstance(value, (int, float)):
        if value < 0:
            raise ValueError("duration must be >= 0")
        return int(value * 1000)
    text = value.strip().replace(" ", "")
    pos, total = 0, 0.0
    for m in _DURATION_PART.finditer(text):
        if m.start() != pos:
            break
        total += float(m.group(1)) * _UNITS_MS[m.group(2)]
        pos = m.end()
    if pos != len(text) or not text:
        raise ValueError(f"invalid duration {value!r} (examples: '200ms', '30s', '1h30m')")
    return int(total)


_SIZE = re.compile(r"^(\d+(?:\.\d+)?)([KMGT]i?)?B?$", re.IGNORECASE)
_SIZE_MULT = {
    None: 1,
    "k": 1000,
    "m": 1000**2,
    "g": 1000**3,
    "t": 1000**4,
    "ki": 1024,
    "mi": 1024**2,
    "gi": 1024**3,
    "ti": 1024**4,
}


def size_bytes(value: str | int | None) -> int:
    """Return a size in bytes ("512Mi", "1G", 1024)."""
    if value is None:
        return 0
    if isinstance(value, int):
        return value
    m = _SIZE.match(value.strip())
    if not m:
        raise ValueError(f"invalid size {value!r} (examples: '512Mi', '2Gi')")
    unit = m.group(2).lower() if m.group(2) else None
    return int(float(m.group(1)) * _SIZE_MULT[unit])
