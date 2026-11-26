import json

import httpx
import pytest

from flyke_worker.logs import LogShipper, Redactor
from flyke_worker.protocol import ProtocolError, WorkerClient


def test_redactor_masks_sensitive_values_in_text_and_json():
    r = Redactor()
    r.add('pa"ss-word')
    r.add("abc")  # too short to mask
    assert r('login with pa"ss-word ok, abc') == "login with ******** ok, abc"
    assert r(json.dumps({"pw": 'pa"ss-word'})) == '{"pw": "********"}'


def test_log_sink_applies_redaction_to_message_and_fields():
    r = Redactor()
    r.add("hunter22")
    shipper = LogShipper(client=None)  # type: ignore[arg-type]
    sink = shipper.sink_for("run", "work", 1, redact=r)
    sink("info", "password=hunter22", {"token": "hunter22"})
    [rec] = list(shipper._buf)
    assert "hunter22" not in rec["message"] and "hunter22" not in rec["fieldsJson"]


async def test_get_secret_over_connect():
    seen = []

    def handler(req: httpx.Request) -> httpx.Response:
        body = json.loads(req.content)
        seen.append((req.url.path, req.headers["authorization"], body))
        if body["key"] == "missing":
            return httpx.Response(404, json={"code": "not_found", "message": "not found"})
        if body["key"] == "denied":
            return httpx.Response(400, json={"code": "invalid_argument", "message": "no"})
        return httpx.Response(
            200, json={"key": body["key"], "value": "v", "sensitive": True, "type": "Shared"}
        )

    c = WorkerClient("http://cp", credential="cred")
    c._http = httpx.AsyncClient(transport=httpx.MockTransport(handler))
    assert await c.get_secret("lease-1", "db") == ("v", True)
    assert await c.get_secret("lease-1", "missing") is None
    with pytest.raises(ProtocolError):
        await c.get_secret("lease-1", "denied")
    assert seen[0] == (
        "/flyke.worker.v1.WorkerService/GetSecret",
        "Bearer cred",
        {"leaseId": "lease-1", "key": "db"},
    )
    await c.close()
