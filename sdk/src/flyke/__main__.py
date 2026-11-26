"""``python -m flyke manifest <file.py:Class> [--root DIR]``: print a workflow manifest.

``python -m flyke discover <DIR>``: print the manifests of every workflow in a
directory, plus per-file import errors (used by the UI playground).

Used by the CLI (``flyke run`` / ``flyke deploy``) to register workflows
without the control plane ever importing user code.
"""

from __future__ import annotations

import argparse
import json
import os
import sys

from ._internal.discover import discover
from ._internal.loader import load, split_entrypoint
from ._internal.manifest import build_manifest


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="python -m flyke")
    sub = parser.add_subparsers(dest="command", required=True)
    m = sub.add_parser("manifest", help="print the manifest of a workflow")
    m.add_argument("target", help="path/to/file.py:ClassName")
    m.add_argument("--root", help="code root (default: the file's directory)")
    d = sub.add_parser("discover", help="print the manifests of all workflows under a directory")
    d.add_argument("root", help="code root")
    args = parser.parse_args(argv)

    if args.command == "discover":
        print(json.dumps(discover(args.root)))
        return 0

    if args.command == "manifest":
        path, attr = split_entrypoint(args.target)
        root = os.path.abspath(args.root or os.path.dirname(os.path.abspath(path)) or ".")
        rel = os.path.relpath(os.path.abspath(path), root) if path.endswith(".py") else path
        entrypoint = f"{rel}:{attr}"
        try:
            defn = load(root, entrypoint)
        except Exception as e:  # noqa: BLE001 - report any import problem to the CLI
            print(json.dumps({"error": f"{type(e).__name__}: {e}"}), file=sys.stderr)
            return 2
        print(json.dumps({"root": root, "manifest": build_manifest(defn, entrypoint)}, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
