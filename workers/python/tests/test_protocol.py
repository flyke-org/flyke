import base64
import json

from flyke.runtime import Command, Failure
from flyke_worker.protocol import Assignment, command_to_wire, history_from_wire


def b64(v):
    return {"encoding": "json/plain", "data": base64.b64encode(json.dumps(v).encode()).decode()}


def test_command_encoding():
    w = command_to_wire(
        Command(3, "ScheduleTask", "provision", payload=["img", "eu"], idempotency_key="k")
    )
    assert (
        w["ref"] == 3
        and w["scheduleTask"]["name"] == "provision"
        and w["scheduleTask"]["idempotencyKey"] == "k"
    )
    assert json.loads(base64.b64decode(w["scheduleTask"]["args"]["data"])) == ["img", "eu"]
    f = command_to_wire(
        Command(9, "FailRun", failure=Failure(type="builtins.RuntimeError", message="boom"))
    )
    assert f["failRun"]["failure"]["type"] == "builtins.RuntimeError"


def test_history_and_assignment_decoding():
    evs = history_from_wire(
        [
            {
                "id": 1,
                "type": "EVENT_TYPE_RUN_STARTED",
                "time": "2026-01-01T00:00:00Z",
                "payload": b64({"service": "api"}),
            },
            {
                "id": 2,
                "type": "EVENT_TYPE_TASK_COMPLETED",
                "time": "2026-01-01T00:00:01.5Z",
                "ref": 1,
                "name": "build",
                "payload": b64("api:1"),
            },
        ]
    )
    assert [e.type for e in evs] == ["RunStarted", "TaskCompleted"]
    assert evs[1].payload == "api:1" and evs[1].time.microsecond == 500000
    a = Assignment.from_wire(
        {
            "leaseId": "l1",
            "workId": "task-1",
            "runId": "r",
            "source": {"entrypoint": "d.py:D", "localRoot": "/x"},
            "inputs": b64({"service": "api"}),
            "task": {
                "taskId": "task-1",
                "ref": 2,
                "name": "migrate_db",
                "attempt": 2,
                "args": b64(["img"]),
            },
        }
    )
    assert not a.is_step and a.args == ["img"] and a.attempt == 2 and a.inputs == {"service": "api"}
