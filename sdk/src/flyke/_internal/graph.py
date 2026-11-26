"""Static view of a workflow body, for diagrams.

``run()`` is plain Python, so there is no declared DAG to draw. This module
parses (never executes) the source of ``run()`` and lays its durable
operations out as a tree:

* ``task``, ``wait``, ``emit`` and ``sleep`` nodes, in call order;
* ``parallel`` blocks for ``asyncio.gather(...)`` (one lane per argument, or
  one lane that fans out over a collection for
  ``gather(*(self.t(x) for x in xs))``);
* ``branch`` (``if``/``elif``/``else``), ``loop`` (``for``/``while``) and
  ``try`` blocks, kept only when they contain durable operations;
* data flow: each call argument lists where its value comes from (workflow
  inputs read as ``self.<input>`` and results of earlier steps assigned to
  variables), so the dashboard can show that ``deploy(image)`` gets ``image``
  from ``build``.

The result is display-only and best effort: it never affects execution, and
any parse problem simply yields no graph. Calls it cannot see statically are
listed in ``dynamic_tasks``; the dashboard places their steps at run time.
"""

from __future__ import annotations

import ast
import enum
import inspect
import itertools
import textwrap
import types
import typing
from collections.abc import Iterable
from typing import Any

from .definition import TaskDef, WorkflowDef

Source = dict[str, Any]
Node = dict[str, Any]
Env = dict[str, list[Source]]

_HELPERS = {"wait_for": "wait", "emit": "emit", "sleep": "sleep"}
_GATHER = {"gather"}


def build_graph(defn: WorkflowDef) -> dict[str, Any] | None:
    run = defn.cls.run
    try:
        lines, first = inspect.getsourcelines(run)
        tree = ast.parse(textwrap.dedent("".join(lines)))
    except (OSError, TypeError, SyntaxError):
        return None
    fn = tree.body[0] if tree.body else None
    if not isinstance(fn, (ast.AsyncFunctionDef, ast.FunctionDef)):
        return None

    a = _Analyzer(defn, first - 1)
    nodes = a.block(fn.body, {})
    compensators = {t.compensation.name for t in defn.tasks.values() if t.compensation is not None}
    out: dict[str, Any] = {
        "tree": nodes,
        "output": {"type": _return_type(run), **(a.output or {"expr": None, "sources": []})},
        "tasks": {name: _signature(t) for name, t in defn.tasks.items()},
    }
    dynamic = [n for n in defn.tasks if n not in a.called and n not in compensators]
    if dynamic:
        out["dynamic_tasks"] = dynamic
    if a.warnings:
        out["warnings"] = a.warnings
    return out


# ------------------------------------------------------------ signatures --


def type_name(tp: Any) -> str:
    """A short rendering of a type annotation: ``List[str]``, ``Optional[int]``…"""
    if tp is inspect.Parameter.empty or tp is inspect.Signature.empty:
        return "Any"
    if tp is None or tp is type(None):
        return "None"
    if isinstance(tp, str):
        return tp
    origin, args = typing.get_origin(tp), typing.get_args(tp)
    if origin is typing.Literal:
        return (
            "Literal["
            + ", ".join(repr(a.value if isinstance(a, enum.Enum) else a) for a in args)
            + "]"
        )
    if origin is typing.Union or origin is types.UnionType:
        options = [a for a in args if a is not type(None)]
        if len(options) == 1 and len(args) == 2:
            return f"Optional[{type_name(options[0])}]"
        return "Union[" + ", ".join(type_name(a) for a in args) + "]"
    if origin is not None:
        name = getattr(origin, "__name__", None) or str(origin).replace("typing.", "")
        name = {
            "list": "List",
            "dict": "Dict",
            "tuple": "Tuple",
            "set": "Set",
            "frozenset": "FrozenSet",
        }.get(name, name)
        return f"{name}[{', '.join(type_name(a) for a in args)}]" if args else name
    if isinstance(tp, type):
        return tp.__name__
    return str(tp).replace("typing.", "")


def _hints(fn: Any) -> dict[str, Any]:
    try:
        return typing.get_type_hints(fn)
    except Exception:  # noqa: BLE001 - unresolvable forward refs: raw annotations
        return dict(getattr(fn, "__annotations__", {}))


def _return_type(fn: Any) -> str:
    return type_name(_hints(fn).get("return", inspect.signature(fn).return_annotation))


def _signature(t: TaskDef) -> dict[str, Any]:
    hints = _hints(t.fn)
    params = []
    for i, p in enumerate(t.signature.parameters.values()):
        if i == 0:
            continue  # self
        param: dict[str, Any] = {"name": p.name, "type": type_name(hints.get(p.name, p.annotation))}
        if p.default is not inspect.Parameter.empty:
            param["default"] = _jsonable(p.default)
        params.append(param)
    out: dict[str, Any] = {
        "params": params,
        "returns": type_name(hints.get("return", t.signature.return_annotation)),
    }
    doc = inspect.getdoc(t.fn)
    if doc:
        out["description"] = doc.strip().splitlines()[0]
    return out


def _jsonable(value: Any) -> Any:
    if isinstance(value, enum.Enum):
        return value.value
    if value is None or isinstance(value, (str, int, float, bool, list, dict)):
        return value
    return repr(value)


# -------------------------------------------------------------- analyzer --


class _Analyzer:
    def __init__(self, defn: WorkflowDef, line_offset: int):
        self.tasks = defn.tasks
        self.inputs = set(defn.fields)
        self.offset = line_offset
        self.ids = itertools.count(1)
        self.warnings: list[str] = []
        self.called: set = set()
        self.output: dict[str, Any] | None = None
        self.params = {n: list(t.signature.parameters)[1:] for n, t in defn.tasks.items()}

    # -------------------------------------------------------- statements --

    def block(self, stmts: Iterable[ast.stmt], env: Env, top: bool = True) -> list[Node]:
        stmts = list(stmts)
        nodes: list[Node] = []
        for i, s in enumerate(stmts):
            nodes.extend(self.stmt(s, env, last=top and i == len(stmts) - 1))
        return nodes

    def stmt(self, s: ast.stmt, env: Env, last: bool) -> list[Node]:
        if isinstance(s, (ast.Assign, ast.AnnAssign, ast.AugAssign)):
            if s.value is None:
                return []
            nodes, sources = self.expr(s.value, env)
            targets = s.targets if isinstance(s, ast.Assign) else [s.target]
            names = [n.id for t in targets for n in ast.walk(t) if isinstance(n, ast.Name)]
            for name in names:
                env[name] = (
                    _merge(env.get(name, []), sources)
                    if isinstance(s, ast.AugAssign)
                    else list(sources)
                )
            produced = _direct_node(s.value, nodes, set(self.tasks) | set(_HELPERS))
            if produced is not None and names:
                produced["assign"] = ", ".join(names)
            return nodes

        if isinstance(s, ast.Expr):
            return self.expr(s.value, env)[0]

        if isinstance(s, ast.Return):
            nodes, sources = self.expr(s.value, env) if s.value is not None else ([], [])
            expr = _unparse(s.value) if s.value is not None else None
            if last:
                self.output = {"expr": expr, "sources": sources}
                return nodes
            return nodes + [
                self.node(
                    "return",
                    s,
                    label=f"return {expr}" if expr else "return",
                    expr=expr,
                    sources=sources,
                )
            ]

        if isinstance(s, ast.If):
            return self.branch(s, env)

        if isinstance(s, (ast.For, ast.AsyncFor)):
            pre, iter_sources = self.expr(s.iter, env)
            inner = dict(env)
            for name in (n.id for n in ast.walk(s.target) if isinstance(n, ast.Name)):
                inner[name] = list(iter_sources)
            body = self.block(s.body, inner, top=False)
            orelse = self.block(s.orelse, inner, top=False)
            _merge_env(env, [inner])
            if not body and not orelse:
                return pre
            label = f"for {_unparse(s.target)} in {_unparse(s.iter)}"
            lanes = [_lane(label, body)] + ([_lane("then (no break)", orelse)] if orelse else [])
            return pre + [self.node("loop", s, label=label, lanes=lanes, sources=iter_sources)]

        if isinstance(s, ast.While):
            pre, sources = self.expr(s.test, env)
            inner = dict(env)
            body = self.block(s.body, inner, top=False)
            _merge_env(env, [inner])
            if not body:
                return pre
            label = f"while {_unparse(s.test)}"
            return pre + [
                self.node("loop", s, label=label, lanes=[_lane(label, body)], sources=sources)
            ]

        if isinstance(s, ast.Try) or type(s).__name__ == "TryStar":
            return self.try_block(s, env)  # type: ignore[arg-type]

        if isinstance(s, (ast.With, ast.AsyncWith)):
            nodes: list[Node] = []
            for item in s.items:
                nodes.extend(self.expr(item.context_expr, env)[0])
            return nodes + self.block(s.body, env, top=False)

        if type(s).__name__ == "Match":
            lanes = []
            subject = _unparse(s.subject)  # type: ignore[attr-defined]
            envs = []
            for case in s.cases:  # type: ignore[attr-defined]
                case_env = dict(env)
                lanes.append(
                    _lane(
                        f"case {_unparse(case.pattern)}", self.block(case.body, case_env, top=False)
                    )
                )
                envs.append(case_env)
            _merge_env(env, envs)
            if not any(lane["nodes"] for lane in lanes):
                return []
            return [
                self.node(
                    "branch",
                    s,
                    label=f"match {subject}",
                    lanes=lanes,
                    sources=self.expr(s.subject, env)[1],
                )
            ]  # type: ignore[attr-defined]

        return []

    def branch(self, s: ast.If, env: Env) -> list[Node]:
        pre, cond_sources = self.expr(s.test, env)
        lanes: list[dict[str, Any]] = []
        envs: list[Env] = []
        current: ast.If | None = s
        first = True
        while current is not None:
            branch_env = dict(env)
            if not first:
                more, extra = self.expr(current.test, env)
                pre.extend(more)
                cond_sources = _merge(cond_sources, extra)
            lanes.append(
                _lane(
                    f"{'if' if first else 'elif'} {_unparse(current.test)}",
                    self.block(current.body, branch_env, top=False),
                )
            )
            envs.append(branch_env)
            first = False
            orelse = current.orelse
            nxt = orelse[0] if len(orelse) == 1 and isinstance(orelse[0], ast.If) else None
            if nxt is None and orelse:
                else_env = dict(env)
                lanes.append(_lane("else", self.block(orelse, else_env, top=False)))
                envs.append(else_env)
            current = nxt
        if not any(lane["nodes"] for lane in lanes):
            return pre
        if not any(lane["label"] == "else" for lane in lanes):
            lanes.append(_lane("else", []))
            envs.append(dict(env))
        _merge_env(env, envs)
        return pre + [
            self.node("branch", s, label=lanes[0]["label"], lanes=lanes, sources=cond_sources)
        ]

    def try_block(self, s: ast.Try, env: Env) -> list[Node]:
        body_env = dict(env)
        body = self.block(s.body, body_env, top=False)
        if s.orelse:
            body = body + self.block(s.orelse, body_env, top=False)
        lanes = [_lane("try", body)]
        envs = [body_env]
        for h in s.handlers:
            h_env = dict(env)
            caught = f"except {_unparse(h.type)}" if h.type is not None else "except"
            lanes.append(_lane(caught, self.block(h.body, h_env, top=False)))
            envs.append(h_env)
        final = self.block(s.finalbody, env, top=False) if s.finalbody else []
        _merge_env(env, envs)
        if not any(lane["nodes"] for lane in lanes[1:]):
            return body + final  # nothing durable happens on errors: a plain sequence
        node = self.node("try", s, label="try", lanes=lanes, sources=[])
        return [node] + (
            [self.node("finally", s, label="finally", lanes=[_lane("finally", final)], sources=[])]
            if final
            else []
        )

    # ------------------------------------------------------- expressions --

    def expr(self, e: ast.AST | None, env: Env) -> tuple[list[Node], list[Source]]:
        """Durable nodes created while evaluating ``e`` (in order) and where its value comes from."""
        if e is None:
            return [], []
        if isinstance(e, ast.Await):
            return self.expr(e.value, env)
        if isinstance(e, ast.Name):
            return [], list(env.get(e.id, []))
        if _is_self_attr(e):
            attr = e.attr  # type: ignore[attr-defined]
            return ([], [{"kind": "input", "name": attr}]) if attr in self.inputs else ([], [])
        if isinstance(e, ast.Call):
            target = _self_method(e.func)
            if target in self.tasks:
                return self.task_call(e, target, env)
            if target in _HELPERS:
                return self.helper_call(e, target, env)
            if _is_gather(e.func):
                return self.gather(e, env)
        if isinstance(e, (ast.Lambda, ast.FunctionDef, ast.AsyncFunctionDef)):
            return [], []
        if isinstance(e, (ast.GeneratorExp, ast.ListComp, ast.SetComp, ast.DictComp)):
            inner = dict(env)
            nodes: list[Node] = []
            sources: list[Source] = []
            for gen in e.generators:
                more, iter_sources = self.expr(gen.iter, inner)
                nodes.extend(more)
                for name in (n.id for n in ast.walk(gen.target) if isinstance(n, ast.Name)):
                    inner[name] = iter_sources
                sources = _merge(sources, iter_sources)
            for elt in [e.key, e.value] if isinstance(e, ast.DictComp) else [e.elt]:
                more, elt_sources = self.expr(elt, inner)
                nodes.extend(more)
                sources = _merge(sources, elt_sources)
            return nodes, sources

        nodes, sources = [], []
        for child in ast.iter_child_nodes(e):
            more, child_sources = self.expr(child, env)
            nodes.extend(more)
            sources = _merge(sources, child_sources)
        return nodes, sources

    def task_call(self, call: ast.Call, name: str, env: Env) -> tuple[list[Node], list[Source]]:
        nodes: list[Node] = []
        args = []
        params = self.params[name]
        for i, arg in enumerate(call.args):
            starred = isinstance(arg, ast.Starred)
            more, sources = self.expr(arg.value if starred else arg, env)  # type: ignore[attr-defined]
            nodes.extend(more)
            pname = (
                f"*{_unparse(arg.value)}"
                if starred
                else (params[i] if i < len(params) else f"arg{i}")
            )  # type: ignore[attr-defined]
            args.append({"name": pname, "expr": _unparse(arg), "sources": sources})
        for kw in call.keywords:
            more, sources = self.expr(kw.value, env)
            nodes.extend(more)
            args.append(
                {
                    "name": kw.arg or f"**{_unparse(kw.value)}",
                    "expr": _unparse(kw.value),
                    "sources": sources,
                }
            )
        self.called.add(name)
        node = self.node("task", call, label=name, task=name, args=args)
        return nodes + [node], [{"kind": "step", "node": node["id"], "task": name}]

    def helper_call(self, call: ast.Call, helper: str, env: Env) -> tuple[list[Node], list[Source]]:
        kind = _HELPERS[helper]
        nodes: list[Node] = []
        args = []
        for i, arg in enumerate(call.args):
            more, sources = self.expr(arg, env)
            nodes.extend(more)
            pname = ("duration" if kind == "sleep" else "event") if i == 0 else f"arg{i}"
            args.append({"name": pname, "expr": _unparse(arg), "sources": sources})
        for kw in call.keywords:
            more, sources = self.expr(kw.value, env)
            nodes.extend(more)
            args.append({"name": kw.arg or "**", "expr": _unparse(kw.value), "sources": sources})
        first = call.args[0] if call.args else None
        event = (
            first.value
            if isinstance(first, ast.Constant) and isinstance(first.value, str)
            else None
        )
        if kind == "sleep":
            label = f"sleep {_unparse(first)}".strip() if first is not None else "sleep"
        else:
            label = event or (_unparse(first) if first is not None else helper)
        fields: dict[str, Any] = {"label": label, "args": args}
        if event:
            fields["event"] = event
        node = self.node(kind, call, **fields)
        produced = [{"kind": "step", "node": node["id"]}] if kind == "wait" else []
        return nodes + [node], produced

    def gather(self, call: ast.Call, env: Env) -> tuple[list[Node], list[Source]]:
        lanes = []
        before: list[Node] = []
        for arg in call.args:
            if isinstance(arg, ast.Starred) and isinstance(
                arg.value, (ast.GeneratorExp, ast.ListComp)
            ):
                comp = arg.value
                inner = dict(env)
                over = []
                for gen in comp.generators:
                    more, iter_sources = self.expr(gen.iter, inner)
                    before.extend(more)
                    for name in (n.id for n in ast.walk(gen.target) if isinstance(n, ast.Name)):
                        inner[name] = iter_sources
                    over.append(f"{_unparse(gen.target)} in {_unparse(gen.iter)}")
                nodes, _ = self.expr(comp.elt, inner)
                lanes.append(_lane("for each " + ", ".join(over), nodes, fan_out=True))
            else:
                starred = isinstance(arg, ast.Starred)
                nodes, _ = self.expr(arg.value if starred else arg, env)  # type: ignore[attr-defined]
                lanes.append(_lane(None, nodes, fan_out=starred))
        lanes = [lane for lane in lanes if lane["nodes"]]
        if not lanes:
            return before, []
        node = self.node("parallel", call, label="in parallel", lanes=lanes, sources=[])
        produced = [
            {"kind": "step", "node": n["id"], **({"task": n["task"]} if n.get("task") else {})}
            for lane in lanes
            for n in lane["nodes"]
            if n["kind"] in ("task", "parallel", "wait")
        ]
        return before + [node], produced

    # ----------------------------------------------------------- helpers --

    def node(self, kind: str, at: ast.AST, **fields: Any) -> Node:
        node: Node = {"id": f"n{next(self.ids)}", "kind": kind}
        lineno = getattr(at, "lineno", None)
        if lineno is not None:
            node["line"] = lineno + self.offset
        node.update(fields)
        return node


def _lane(label: str | None, nodes: list[Node], fan_out: bool = False) -> dict[str, Any]:
    lane: dict[str, Any] = {"label": label, "nodes": nodes}
    if fan_out:
        lane["fan_out"] = True
    return lane


def _direct_node(value: ast.AST, nodes: list[Node], durable: set) -> Node | None:
    """The node whose result *is* the assigned value (``x = await self.t()``), not one nested in it."""
    while isinstance(value, ast.Await):
        value = value.value
    if not nodes or not isinstance(value, ast.Call):
        return None
    if _self_method(value.func) in durable or _is_gather(value.func):
        return nodes[-1]
    return None


def _self_method(func: ast.AST) -> str | None:
    if (
        isinstance(func, ast.Attribute)
        and isinstance(func.value, ast.Name)
        and func.value.id == "self"
    ):
        return func.attr
    return None


def _is_self_attr(e: ast.AST) -> bool:
    return isinstance(e, ast.Attribute) and isinstance(e.value, ast.Name) and e.value.id == "self"


def _is_gather(func: ast.AST) -> bool:
    return (isinstance(func, ast.Attribute) and func.attr in _GATHER) or (
        isinstance(func, ast.Name) and func.id in _GATHER
    )


def _merge(a: list[Source], b: list[Source]) -> list[Source]:
    out = list(a)
    for s in b:
        if s not in out:
            out.append(s)
    return out


def _merge_env(env: Env, branches: list[Env]) -> None:
    """After a branch, a variable may come from any branch that assigned it."""
    for branch in branches:
        for name, sources in branch.items():
            if env.get(name) is not sources:
                env[name] = _merge(env.get(name, []), sources)


def _unparse(node: ast.AST | None) -> str:
    if node is None:
        return ""
    try:
        return ast.unparse(node)
    except Exception:  # noqa: BLE001 - display only
        return "…"
