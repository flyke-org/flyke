"""Client for the flyke.worker.v1 protocol over Connect (HTTP/1.1 + JSON).

Each RPC is ``POST {url}/flyke.worker.v1.WorkerService/{Method}`` with a
protojson body. This module is the only place that knows the wire format;
everything else works with plain Python values.
"""

from __future__ import annotations

import asyncio
import base64
import json
from dataclasses import dataclass
from datetime import datetime
from typing import Any

import httpx

from flyke.runtime import Command, Failure, HistoryEvent

SERVICE = "flyke.worker.v1.WorkerService"
ENCODING = "json/plain"


class ProtocolError(Exception):
    def __init__(self, code: str, message: str, status: int = 0):
        super().__init__(f"{code}: {message}")
        self.code, self.message, self.status = code, message, status

    @property
    def unauthenticated(self) -> bool:
        return self.code == "unauthenticated"

    @property
    def stale(self) -> bool:
        return self.code in ("failed_precondition", "not_found")

    @property
    def retryable(self) -> bool:
        return self.code in (
            "unavailable",
            "deadline_exceeded",
            "internal",
            "unknown",
            "resource_exhausted",
            "aborted",
        )


# --------------------------------------------------------------- payloads ----


def encode_payload(value: bytes) -> dict[str, str]:
    return {"encoding": ENCODING, "data": base64.b64encode(value).decode()}


def decode_payload(p: dict[str, Any] | None) -> Any:
    if not p or not p.get("data"):
        return None
    enc = p.get("encoding") or ENCODING
    if enc != ENCODING:
        raise ValueError(f"unsupported payload encoding {enc!r}")
    return json.loads(base64.b64decode(p["data"]))


def _time(s: str | None) -> datetime:
    return (
        datetime.fromisoformat(s.replace("Z", "+00:00"))
        if s
        else datetime.fromtimestamp(0).astimezone()
    )


def failure_to_wire(f: Failure) -> dict[str, Any]:
    return {
        "type": f.type,
        "mro": f.mro,
        "message": f.message,
        "stack": f.stack,
        "nonRetryable": f.non_retryable,
    }


def failure_from_wire(d: dict[str, Any] | None) -> Failure | None:
    if not d:
        return None
    return Failure(
        type=d.get("type", ""),
        message=d.get("message", ""),
        mro=list(d.get("mro", [])),
        stack=d.get("stack", ""),
        non_retryable=bool(d.get("nonRetryable", False)),
    )


def _event_type(enum_name: str) -> str:
    # EVENT_TYPE_TASK_COMPLETED -> TaskCompleted
    parts = enum_name.removeprefix("EVENT_TYPE_").split("_")
    return "".join(p.capitalize() for p in parts)


def history_from_wire(events: list[dict[str, Any]]) -> list[HistoryEvent]:
    return [
        HistoryEvent(
            id=int(e.get("id", 0)),
            type=_event_type(e.get("type", "")),
            time=_time(e.get("time")),
            ref=int(e.get("ref", 0)),
            name=e.get("name", ""),
            payload=decode_payload(e.get("payload")),
            failure=failure_from_wire(e.get("failure")),
        )
        for e in events
    ]


def command_to_wire(c: Command) -> dict[str, Any]:
    from flyke.runtime import dumps

    out: dict[str, Any] = {"ref": c.ref}
    if c.kind == "ScheduleTask":
        out["scheduleTask"] = {
            "name": c.name,
            "args": encode_payload(dumps(c.payload)),
            "idempotencyKey": c.idempotency_key,
        }
    elif c.kind == "StartTimer":
        out["startTimer"] = {"durationMs": c.duration_ms}
    elif c.kind == "WaitEvent":
        out["waitEvent"] = {
            "name": c.name,
            "where": encode_payload(dumps(c.where or {})),
            "timeoutMs": c.duration_ms,
        }
    elif c.kind == "EmitEvent":
        out["emitEvent"] = {"name": c.name, "payload": encode_payload(dumps(c.payload or {}))}
    elif c.kind == "CompleteRun":
        out["completeRun"] = {"result": encode_payload(dumps(c.payload))}
    elif c.kind == "FailRun":
        assert c.failure is not None
        out["failRun"] = {"failure": failure_to_wire(c.failure)}
    else:
        raise ValueError(f"unknown command kind {c.kind}")
    return out


@dataclass
class Assignment:
    lease_id: str
    work_id: str
    run_id: str
    namespace: str
    workflow: str
    entrypoint: str
    local_root: str
    bundle_digest: str
    inputs: dict[str, Any]
    deadline: datetime | None
    # task
    is_step: bool
    task_id: str = ""
    ref: int = 0
    name: str = ""
    attempt: int = 1
    args: list[Any] | None = None
    idempotency_key: str = ""
    compensation: bool = False
    compensated_task: str = ""
    compensated_result: Any = None
    # step
    history: list[HistoryEvent] | None = None

    @classmethod
    def from_wire(cls, a: dict[str, Any]) -> Assignment:
        src = a.get("source") or {}
        base = dict(
            lease_id=a["leaseId"],
            work_id=a.get("workId", ""),
            run_id=a.get("runId", ""),
            namespace=a.get("namespace", "default"),
            workflow=a.get("workflow", ""),
            entrypoint=src.get("entrypoint", ""),
            local_root=src.get("localRoot", ""),
            bundle_digest=src.get("bundleDigest", ""),
            inputs=decode_payload(a.get("inputs")) or {},
            deadline=_time(a["deadline"]) if a.get("deadline") else None,
        )
        if "step" in a:
            return cls(
                is_step=True,
                history=history_from_wire((a["step"] or {}).get("history", [])),
                **base,
            )
        t = a.get("task") or {}
        return cls(
            is_step=False,
            task_id=t.get("taskId", ""),
            ref=int(t.get("ref", 0)),
            name=t.get("name", ""),
            attempt=int(t.get("attempt", 1)),
            args=decode_payload(t.get("args")) or [],
            idempotency_key=t.get("idempotencyKey", ""),
            compensation=bool(t.get("compensation", False)),
            compensated_task=t.get("compensatedTask", ""),
            compensated_result=decode_payload(t.get("compensatedResult")),
            **base,
        )


# ------------------------------------------------------------------ client ----


class WorkerClient:
    def __init__(self, url: str, credential: str = "", timeout: float = 15.0, verify: Any = True):
        self.url = url.rstrip("/")
        self.credential = credential
        self._http = httpx.AsyncClient(
            timeout=timeout, verify=verify, headers={"Connect-Protocol-Version": "1"}
        )

    async def close(self) -> None:
        await self._http.aclose()

    async def call(
        self, method: str, body: dict[str, Any], timeout: float | None = None
    ) -> dict[str, Any]:
        headers = {"Content-Type": "application/json"}
        if self.credential:
            headers["Authorization"] = f"Bearer {self.credential}"
        try:
            resp = await self._http.post(
                f"{self.url}/{SERVICE}/{method}",
                content=json.dumps(body),
                headers=headers,
                timeout=timeout if timeout is not None else httpx.USE_CLIENT_DEFAULT,
            )
        except (TimeoutError, httpx.TransportError) as e:
            raise ProtocolError("unavailable", f"{type(e).__name__}: {e}") from e
        if resp.status_code != 200:
            try:
                err = resp.json()
                raise ProtocolError(
                    err.get("code", "unknown"), err.get("message", resp.text), resp.status_code
                )
            except ValueError:
                raise ProtocolError("unknown", resp.text, resp.status_code) from None
        return resp.json() if resp.content else {}

    async def call_reliably(
        self, method: str, body: dict[str, Any], attempts: int = 8
    ) -> dict[str, Any] | None:
        """Retry transient errors (reports must not be lost). Returns None on stale leases."""
        delay = 0.2
        for i in range(attempts):
            try:
                return await self.call(method, body)
            except ProtocolError as e:
                if e.stale:
                    return None
                if not e.retryable or i == attempts - 1:
                    raise
                await asyncio.sleep(delay)
                delay = min(delay * 2, 5.0)
        return None

    # Convenience wrappers ------------------------------------------------

    async def register(self, **body: Any) -> dict[str, Any]:
        return await self.call("Register", body)

    async def heartbeat(self, free_slots: int, leases: list[str]) -> dict[str, Any]:
        return await self.call("Heartbeat", {"freeSlots": free_slots, "activeLeases": leases})

    async def poll(self, max_items: int, wait_seconds: int) -> list[Assignment]:
        resp = await self.call(
            "Poll", {"maxItems": max_items, "waitSeconds": wait_seconds}, timeout=wait_seconds + 15
        )
        return [Assignment.from_wire(a) for a in resp.get("assignments", [])]

    async def ack(self, lease: str) -> bool:
        resp = await self.call_reliably("AckTask", {"leaseId": lease}, attempts=4)
        return bool(resp and resp.get("proceed"))

    async def complete_task(self, lease: str, result: bytes) -> None:
        await self.call_reliably(
            "CompleteTask", {"leaseId": lease, "result": encode_payload(result)}
        )

    async def fail_task(self, lease: str, f: Failure) -> None:
        await self.call_reliably("FailTask", {"leaseId": lease, "failure": failure_to_wire(f)})

    async def complete_step(self, lease: str, commands: list[Command]) -> None:
        await self.call_reliably(
            "CompleteStep", {"leaseId": lease, "commands": [command_to_wire(c) for c in commands]}
        )

    async def fail_step(self, lease: str, f: Failure) -> None:
        await self.call_reliably("FailStep", {"leaseId": lease, "failure": failure_to_wire(f)})

    async def push_logs(self, records: list[dict[str, Any]]) -> None:
        await self.call("PushLogs", {"records": records})

    async def fetch_bundle(self, digest: str) -> bytes:
        resp = await self.call("FetchBundle", {"digest": digest}, timeout=120)
        return base64.b64decode(resp.get("data", ""))

    async def get_secret(self, lease: str, key: str) -> tuple[str, bool] | None:
        """Return (value, sensitive) for the task holding ``lease``, or None if no such secret."""
        try:
            resp = await self.call("GetSecret", {"leaseId": lease, "key": key})
        except ProtocolError as e:
            if e.code == "not_found":
                return None
            raise
        return resp.get("value", ""), bool(resp.get("sensitive", False))

    async def deregister(self) -> None:
        await self.call("Deregister", {})
