import os
import time

from flyke._internal.discover import discover
from flyke._internal.loader import load

WF = """from flyke import workflow
from helpers import VALUE

@workflow
class Wf:
    async def run(self) -> str:
        return VALUE
"""


def _write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(text)
    # Make sure the mtime moves even on coarse-grained filesystems.
    t = time.time() + _write.bump
    _write.bump += 1
    os.utime(path, (t, t))


_write.bump = 1


def _value(defn):
    return defn.cls.run.__globals__["VALUE"]


def test_helper_change_reloads_entrypoint(tmp_path):
    root = str(tmp_path / "a")
    _write(os.path.join(root, "main.py"), WF)
    _write(os.path.join(root, "helpers.py"), "VALUE = 'one'\n")
    first = load(root, "main.py:Wf")
    assert load(root, "main.py:Wf") is first  # unchanged: cached
    _write(os.path.join(root, "helpers.py"), "VALUE = 'two'\n")
    second = load(root, "main.py:Wf")
    assert second is not first
    assert _value(second) == "two"


def test_same_module_names_in_two_roots(tmp_path):
    a, b = str(tmp_path / "a"), str(tmp_path / "b")
    for root, value in ((a, "from-a"), (b, "from-b")):
        _write(os.path.join(root, "main.py"), WF)
        _write(os.path.join(root, "helpers.py"), f"VALUE = {value!r}\n")
    da = load(a, "main.py:Wf")
    db = load(b, "main.py:Wf")
    assert _value(da) == "from-a"
    assert _value(db) == "from-b"


def test_discover_reports_workflows_and_errors(tmp_path):
    root = str(tmp_path)
    _write(os.path.join(root, "main.py"), WF)
    _write(os.path.join(root, "helpers.py"), "VALUE = 1\n")
    _write(os.path.join(root, "broken.py"), "x = 1\nraise RuntimeError('boom')\n")
    out = discover(root)
    assert [w["entrypoint"] for w in out["workflows"]] == ["main.py:Wf"]
    assert out["errors"] == [{"file": "broken.py", "line": 2, "error": "RuntimeError: boom"}]
