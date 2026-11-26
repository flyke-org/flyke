"""Static workflow graph extracted from run() for the dashboard diagram."""

from __future__ import annotations

import asyncio

from flyke import task, workflow
from flyke.runtime import build_manifest, definition_of


@workflow
class Shape:
    regions: list = ["a", "b"]
    prod: bool = False

    @task
    async def build(self) -> str:
        return "img"

    @task
    async def deploy(self, image: str, region: str) -> str:
        return region

    @task
    async def check(self, a: str, b: str) -> None:
        return None

    @task
    async def notify(self) -> None:
        return None

    async def run(self) -> None:
        image = await self.build()
        await asyncio.gather(*(self.deploy(image, r) for r in self.regions))
        await asyncio.gather(self.check("x", "y"), self.notify())
        if self.prod:
            await self.wait_for("approved", where={"x": 1})
        for _ in range(2):
            await self.sleep("1s")
        await self.emit("done")


def graph():
    return build_manifest(definition_of(Shape), "t.py:Shape")["graph"]


def kinds(nodes):
    return [(n["kind"], n.get("task") or n["label"]) for n in nodes]


def test_tree_follows_run_in_call_order():
    tree = graph()["tree"]
    assert kinds(tree) == [
        ("task", "build"),
        ("parallel", "in parallel"),
        ("parallel", "in parallel"),
        ("branch", "if self.prod"),
        ("loop", "for _ in range(2)"),
        ("emit", "done"),
    ]
    fan = tree[1]["lanes"]
    assert (
        len(fan) == 1
        and fan[0]["fan_out"] is True
        and fan[0]["label"] == "for each r in self.regions"
    )
    assert kinds(fan[0]["nodes"]) == [("task", "deploy")]
    assert [kinds(lane["nodes"]) for lane in tree[2]["lanes"]] == [
        [("task", "check")],
        [("task", "notify")],
    ]
    assert [lane["label"] for lane in tree[3]["lanes"]] == ["if self.prod", "else"]
    assert kinds(tree[3]["lanes"][0]["nodes"]) == [("wait", "approved")]
    assert kinds(tree[4]["lanes"][0]["nodes"]) == [("sleep", "sleep '1s'")]


def test_arguments_know_where_their_values_come_from():
    tree = graph()["tree"]
    build = tree[0]
    assert build["assign"] == "image"
    deploy = tree[1]["lanes"][0]["nodes"][0]
    image, region = deploy["args"]
    assert (image["name"], image["expr"]) == ("image", "image")
    assert image["sources"] == [{"kind": "step", "node": build["id"], "task": "build"}]
    assert (region["name"], region["sources"]) == ("region", [{"kind": "input", "name": "regions"}])
    assert tree[3]["sources"] == [{"kind": "input", "name": "prod"}]


def test_signatures_and_output():
    g = graph()
    assert g["tasks"]["deploy"] == {
        "params": [{"name": "image", "type": "str"}, {"name": "region", "type": "str"}],
        "returns": "str",
    }
    assert g["output"] == {"type": "None", "expr": None, "sources": []}
    assert "dynamic_tasks" not in g


def test_task_params_are_listed():
    m = build_manifest(definition_of(Shape), "t.py:Shape")
    assert m["tasks"]["deploy"]["params"] == ["image", "region"]
    assert "params" not in m["tasks"]["build"]
