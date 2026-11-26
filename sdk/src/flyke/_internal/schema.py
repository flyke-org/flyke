"""Python type hints → JSON Schema (for input validation and UI forms)."""

from __future__ import annotations

import dataclasses
import enum
import types
import typing
from typing import Any, Union

from .codec import to_jsonable


def type_schema(tp: Any) -> dict[str, Any]:
    if tp is Any:
        return {}
    if tp is type(None) or tp is None:
        return {"type": "null"}
    if tp is str:
        return {"type": "string"}
    if tp is bool:
        return {"type": "boolean"}
    if tp is int:
        return {"type": "integer"}
    if tp is float:
        return {"type": "number"}
    if tp in (dict, dict):
        return {"type": "object"}
    if tp in (list, tuple, set, frozenset):
        return {"type": "array"}
    if isinstance(tp, type) and issubclass(tp, enum.Enum):
        return {"enum": [m.value for m in tp]}
    if dataclasses.is_dataclass(tp) and isinstance(tp, type):
        hints = typing.get_type_hints(tp)
        props, required = {}, []
        for f in dataclasses.fields(tp):
            props[f.name] = type_schema(hints.get(f.name, Any))
            if f.default is dataclasses.MISSING and f.default_factory is dataclasses.MISSING:
                required.append(f.name)
        out: dict[str, Any] = {"type": "object", "properties": props}
        if required:
            out["required"] = required
        return out

    origin = typing.get_origin(tp)
    args = typing.get_args(tp)
    if origin is typing.Literal:
        out = {"enum": list(args)}
        kinds = {type(a) for a in args}
        if len(kinds) == 1:
            out.update(type_schema(kinds.pop()))
        return out
    if origin in (list, set, frozenset, typing.Sequence, list) or (
        origin is tuple and len(args) == 2 and args[1] is Ellipsis
    ):
        return {"type": "array", "items": type_schema(args[0])} if args else {"type": "array"}
    if origin is tuple:
        return {
            "type": "array",
            "prefixItems": [type_schema(a) for a in args],
            "minItems": len(args),
            "maxItems": len(args),
        }
    if origin in (dict, typing.Mapping, dict):
        out = {"type": "object"}
        if len(args) == 2:
            out["additionalProperties"] = type_schema(args[1])
        return out
    if origin is Union or (hasattr(types, "UnionType") and origin is types.UnionType):
        return {"anyOf": [type_schema(a) for a in args]}
    if origin is typing.Annotated:
        return type_schema(args[0])
    return {}


def inputs_schema(fields: dict[str, Any]) -> dict[str, Any]:
    """Schema of a workflow's inputs from its Field objects."""
    props: dict[str, Any] = {}
    required = []
    for f in fields.values():
        s = type_schema(f.type)
        if not f.required:
            s = dict(s, default=to_jsonable(f.default))
        else:
            required.append(f.name)
        props[f.name] = s
    out: dict[str, Any] = {"type": "object", "properties": props, "additionalProperties": False}
    if required:
        out["required"] = required
    return out
