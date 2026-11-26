"""``flyke-worker``: register this host as a worker and run it.

flyke-worker join --url https://flyke.example.com --token flyke_jt_xxx.yyy --labels region=eu
flyke-worker start          # reuse the saved credential
flyke-worker leave          # deregister and forget the credential
"""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import os
import platform
import socket
import stat
import sys
from typing import Any

from flyke.runtime import RUNTIME

from . import PROTOCOL_VERSION, __version__
from .agent import Agent
from .protocol import ProtocolError, WorkerClient

log = logging.getLogger("flyke.worker")

DEFAULT_STATE_DIR = os.environ.get("FLYKE_WORKER_HOME", os.path.expanduser("~/.flyke-worker"))


def _labels(s: str) -> dict[str, str]:
    out: dict[str, str] = {}
    for part in filter(None, (p.strip() for p in s.split(","))):
        k, sep, v = part.partition("=")
        if not sep or not k:
            raise argparse.ArgumentTypeError(f"invalid label {part!r}; expected key=value")
        out[k] = v
    return out


def _memory_bytes() -> int:
    try:
        return os.sysconf("SC_PAGE_SIZE") * os.sysconf("SC_PHYS_PAGES")
    except (ValueError, OSError, AttributeError):
        return 0


def _state_path(state_dir: str) -> str:
    return os.path.join(state_dir, "credentials.json")


def _save_state(state_dir: str, state: dict[str, Any]) -> None:
    os.makedirs(state_dir, mode=0o700, exist_ok=True)
    path = _state_path(state_dir)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, stat.S_IRUSR | stat.S_IWUSR)
    with os.fdopen(fd, "w") as f:
        json.dump(state, f, indent=2)


def _load_state(state_dir: str) -> dict[str, Any] | None:
    try:
        with open(_state_path(state_dir)) as f:
            return json.load(f)
    except FileNotFoundError:
        return None


async def _register(args: argparse.Namespace) -> dict[str, Any]:
    client = WorkerClient(args.url)
    try:
        resp = await client.register(
            joinToken=args.token,
            hostname=socket.gethostname(),
            name=args.name or "",
            labels=args.labels,
            runtimes=[RUNTIME],
            protocolVersion=PROTOCOL_VERSION,
            agentVersion=f"flyke-worker/{__version__} ({platform.system().lower()})",
            capacity={
                "slots": args.slots,
                "resources": {"cpu": float(os.cpu_count() or 1), "memoryBytes": _memory_bytes()},
            },
        )
    finally:
        await client.close()
    return {
        "url": args.url,
        "worker_name": resp["workerName"],
        "credential": resp["credential"],
        "labels": resp.get("labels", {}),
        "heartbeat_interval": int(resp.get("heartbeatIntervalSeconds", 5)),
    }


async def _run(state: dict[str, Any], args: argparse.Namespace) -> int:
    client = WorkerClient(state["url"], state["credential"])
    agent = Agent(
        client,
        name=state["worker_name"],
        slots=args.slots,
        bundle_dir=os.path.join(args.state_dir, "bundles"),
        heartbeat_interval=float(state.get("heartbeat_interval", 5)),
    )
    try:
        code = await agent.run()
        if args.ephemeral and not agent.revoked:
            try:
                await client.deregister()
            except ProtocolError:
                pass
        return code
    finally:
        await client.close()


def cmd_join(args: argparse.Namespace) -> int:
    state = None if (args.force or args.ephemeral) else _load_state(args.state_dir)
    if state and state.get("url") == args.url:
        log.info("already registered as %s (use --force to re-register)", state["worker_name"])
    else:
        if not args.token:
            log.error("--token is required to join")
            return 2
        try:
            state = asyncio.run(_register(args))
        except ProtocolError as e:
            log.error("registration failed: %s", e)
            return 1
        if not args.ephemeral:
            _save_state(args.state_dir, state)
        log.info("registered as %s with labels %s", state["worker_name"], state["labels"])
    if args.no_start:
        return 0
    return asyncio.run(_run(state, args))


def cmd_start(args: argparse.Namespace) -> int:
    state = _load_state(args.state_dir)
    if not state:
        log.error("not registered; run `flyke-worker join --url ... --token ...` first")
        return 2
    return asyncio.run(_run(state, args))


def cmd_leave(args: argparse.Namespace) -> int:
    state = _load_state(args.state_dir)
    if not state:
        log.info("not registered")
        return 0

    async def leave() -> None:
        client = WorkerClient(state["url"], state["credential"])
        try:
            await client.deregister()
        finally:
            await client.close()

    try:
        asyncio.run(leave())
    except ProtocolError as e:
        log.warning("deregistration failed (%s); forgetting the credential anyway", e)
    os.remove(_state_path(args.state_dir))
    log.info("left the cluster")
    return 0


def main(argv: list | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="flyke-worker", description="Flyke worker for Python workflows"
    )
    parser.add_argument(
        "--state-dir", default=DEFAULT_STATE_DIR, help="credential and bundle cache directory"
    )
    parser.add_argument("-v", "--verbose", action="store_true")
    sub = parser.add_subparsers(dest="command", required=True)

    def common(p: argparse.ArgumentParser) -> None:
        p.add_argument(
            "--slots",
            type=int,
            default=int(os.environ.get("FLYKE_WORKER_SLOTS", os.cpu_count() or 4)),
            help="max concurrent work items",
        )

    j = sub.add_parser("join", help="register with a join token, then start")
    j.add_argument(
        "--url", required=True, default=os.environ.get("FLYKE_URL"), help="control plane URL"
    )
    j.add_argument(
        "--token", default=os.environ.get("FLYKE_JOIN_TOKEN"), help="join token (flyke_jt_…)"
    )
    j.add_argument("--name", help="worker name (default: hostname + suffix)")
    j.add_argument("--labels", type=_labels, default={}, help="comma-separated key=value labels")
    j.add_argument("--force", action="store_true", help="re-register even if a credential is saved")
    j.add_argument("--no-start", action="store_true", help="register only")
    j.add_argument(
        "--ephemeral", action="store_true", help="do not persist the credential; deregister on exit"
    )
    common(j)
    j.set_defaults(fn=cmd_join)

    s = sub.add_parser("start", help="start with the saved credential")
    common(s)
    s.set_defaults(fn=cmd_start, ephemeral=False)

    leave = sub.add_parser("leave", help="deregister this worker")
    leave.set_defaults(fn=cmd_leave)

    args = parser.parse_args(argv)
    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        stream=sys.__stderr__,
    )
    logging.getLogger("httpx").setLevel(logging.WARNING)
    return args.fn(args)
