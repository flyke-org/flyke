"""JSON encoding of values crossing the workflow/task boundary."""

from __future__ import annotations

import dataclasses
import enum
import json
from datetime import date, datetime, timedelta
from decimal import Decimal
from pathlib import PurePath
from typing import Any
from uuid import UUID


def to_jsonable(value: Any) -> Any:
    if value is None or isinstance(value, (bool, int, float, str)):
        return value
    if isinstance(value, enum.Enum):
        return to_jsonable(value.value)
    if isinstance(value, (datetime, date)):
        return value.isoformat()
    if isinstance(value, timedelta):
        return value.total_seconds()
    if isinstance(value, (UUID, Decimal, PurePath)):
        return str(value)
    if dataclasses.is_dataclass(value) and not isinstance(value, type):
        return {f.name: to_jsonable(getattr(value, f.name)) for f in dataclasses.fields(value)}
    if isinstance(value, dict):
        return {str(k): to_jsonable(v) for k, v in value.items()}
    if isinstance(value, (list, tuple, set, frozenset)):
        return [to_jsonable(v) for v in value]
    if hasattr(value, "model_dump"):  # pydantic v2
        return to_jsonable(value.model_dump(mode="json"))
    raise TypeError(
        f"value of type {type(value).__name__} is not JSON-serialisable; return plain data from tasks"
    )


def dumps(value: Any) -> bytes:
    return json.dumps(to_jsonable(value), separators=(",", ":"), ensure_ascii=False).encode()


def loads(data: bytes | str | None) -> Any:
    if data is None or data == b"" or data == "":
        return None
    return json.loads(data)
