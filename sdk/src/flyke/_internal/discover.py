"""Find every ``@workflow`` class under a code root (used by the UI playground).

Each ``.py`` file is imported the same way the worker imports entrypoints, so a
discovered entrypoint loads identically at run time. Import problems are
reported per file, with the line in that file when it can be found, instead of
aborting discovery.
"""

from __future__ import annotations

import importlib.util
import os
import sys
import traceback
from typing import Any

from .definition import WorkflowDef
from .manifest import build_manifest

_SKIP_DIRS = {".git", ".venv", "venv", "__pycache__", "node_modules"}


def _python_files(root: str) -> list[str]:
    out: list[str] = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in _SKIP_DIRS and not d.startswith("."))
        for f in sorted(filenames):
            if f.endswith(".py"):
                out.append(os.path.relpath(os.path.join(dirpath, f), root))
    return out


def _error_line(exc: BaseException, path: str) -> int | None:
    if isinstance(exc, SyntaxError) and exc.filename and os.path.abspath(exc.filename) == path:
        return exc.lineno
    line = None
    for frame in traceback.extract_tb(exc.__traceback__):
        if os.path.abspath(frame.filename) == path:
            line = frame.lineno
    return line


def discover(root: str) -> dict[str, Any]:
    root = os.path.abspath(root)
    if root not in sys.path:
        sys.path.insert(0, root)
    workflows: list[dict[str, Any]] = []
    errors: list[dict[str, Any]] = []
    for rel in _python_files(root):
        path = os.path.join(root, rel)
        mod_name = rel[: -len(".py")].replace(os.sep, ".")
        try:
            module = sys.modules.get(mod_name)
            if module is None or os.path.abspath(getattr(module, "__file__", "") or "") != path:
                spec = importlib.util.spec_from_file_location(mod_name, path)
                if spec is None or spec.loader is None:
                    raise ImportError(f"cannot load {rel}")
                module = importlib.util.module_from_spec(spec)
                sys.modules[mod_name] = module
                spec.loader.exec_module(module)
            for attr, obj in sorted(vars(module).items()):
                defn = getattr(obj, "__flyke_workflow__", None)
                if (
                    not isinstance(defn, WorkflowDef)
                    or getattr(obj, "__module__", None) != mod_name
                ):
                    continue
                entrypoint = f"{rel.replace(os.sep, '/')}:{attr}"
                try:
                    workflows.append(
                        {"entrypoint": entrypoint, "manifest": build_manifest(defn, entrypoint)}
                    )
                except Exception as e:  # noqa: BLE001 - a bad option must not hide other workflows
                    errors.append(
                        {
                            "file": rel,
                            "line": _error_line(e, path),
                            "error": f"{attr}: {type(e).__name__}: {e}",
                        }
                    )
        except Exception as e:  # noqa: BLE001 - report any import problem to the UI
            sys.modules.pop(mod_name, None)
            errors.append(
                {
                    "file": rel.replace(os.sep, "/"),
                    "line": _error_line(e, path),
                    "error": f"{type(e).__name__}: {e}",
                }
            )
    return {"root": root, "workflows": workflows, "errors": errors}
