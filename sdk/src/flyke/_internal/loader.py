"""Load workflow classes from "<path.py>:<Class>" or "<module>:<Class>" entrypoints."""

from __future__ import annotations

import importlib
import importlib.util
import os
import sys
import threading
from collections.abc import Iterator

from .definition import WorkflowDef, definition_of

_lock = threading.Lock()
_cache: dict[tuple[str, str], tuple[float, WorkflowDef]] = {}
# root -> {module file: mtime} for the user modules imported from that root.
# A worker serves many roots (playground projects, bundle versions) in one
# process, and a change to any of a root's modules must reload it.
_deps: dict[str, dict[str, float]] = {}


def _mtime(path: str) -> float:
    try:
        return os.stat(path).st_mtime
    except OSError:
        return -1.0


def _under(path: str, root: str) -> bool:
    return path == root or path.startswith(root + os.sep)


def _user_modules(root: str) -> Iterator[tuple[str, str]]:
    """(name, file) of modules imported from root's own code, excluding
    packages installed inside it (a .venv, an editable SDK under src/…)."""
    nested = [os.path.abspath(p) for p in sys.path if p]
    nested = [p for p in nested if p != root and _under(p, root)]
    for name, mod in list(sys.modules.items()):
        f = getattr(mod, "__file__", None)
        if not f:
            continue
        f = os.path.abspath(f)
        if _under(f, root) and not any(_under(f, p) for p in nested):
            yield name, f


def _forget(root: str) -> None:
    """Drop root's modules so the next import re-executes them."""
    for name, _ in list(_user_modules(root)):
        sys.modules.pop(name, None)


def split_entrypoint(entrypoint: str) -> tuple[str, str]:
    target, sep, attr = entrypoint.rpartition(":")
    if not sep or not target or not attr:
        raise ValueError(f"invalid entrypoint {entrypoint!r}; expected 'path/to/file.py:ClassName'")
    return target, attr


def load(root: str, entrypoint: str) -> WorkflowDef:
    """Import the workflow class. File-based modules are reloaded when they change."""
    target, attr = split_entrypoint(entrypoint)
    root = os.path.abspath(root)
    with _lock:
        # This root's modules must win imports over other roots' same-named ones.
        if not sys.path or sys.path[0] != root:
            if root in sys.path:
                sys.path.remove(root)
            sys.path.insert(0, root)
        if target.endswith(".py"):
            path = os.path.normpath(os.path.join(root, target))
            if os.path.commonpath([root, path]) != root:
                raise ValueError(f"entrypoint {entrypoint!r} escapes its root")
            if any(_mtime(f) != m for f, m in _deps.get(root, {}).items()):
                # A module of this root changed: reload everything from it.
                for k in [k for k in _cache if _under(k[0], root)]:
                    del _cache[k]
                _deps.pop(root, None)
            mtime = os.stat(path).st_mtime
            key = (path, attr)
            cached = _cache.get(key)
            if cached and cached[0] == mtime:
                return cached[1]
            # Re-import with a clean slate: this root's modules (possibly
            # stale) and other roots' modules (which could shadow ours).
            for other in [r for r in _deps if r != root]:
                _forget(other)
            _forget(root)
            # Name the module after its path relative to the root so that
            # qualified names (e.g. exception types in Retry(on=...)) are the
            # same on every machine, whatever the absolute location.
            mod_name = os.path.relpath(path, root)[: -len(".py")].replace(os.sep, ".")
            spec = importlib.util.spec_from_file_location(mod_name, path)
            if spec is None or spec.loader is None:
                raise ImportError(f"cannot load {path}")
            module = importlib.util.module_from_spec(spec)
            sys.modules[mod_name] = module
            spec.loader.exec_module(module)
            defn = definition_of(getattr(module, attr))
            _cache[key] = (mtime, defn)
            _deps.setdefault(root, {}).update({f: _mtime(f) for _, f in _user_modules(root)})
            return defn
        module = importlib.import_module(target)
        return definition_of(getattr(module, attr))
