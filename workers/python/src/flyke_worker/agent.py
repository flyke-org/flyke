"""The worker agent (≈ kubelet): heartbeats, long-polls for work, executes it
in isolated threads, and reports facts back to the control plane."""

from __future__ import annotations

import asyncio
import concurrent.futures
import logging
import signal
import threading
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Any

from flyke.runtime import Failure, RunContext, dumps, execute_task, load, replay_step

from .bundles import BundleCache
from .logs import LogShipper, Redactor, capture_into, install_capture
from .protocol import Assignment, ProtocolError, WorkerClient

log = logging.getLogger("flyke.worker")


class Revoked(Exception):
    """The control plane no longer recognises this worker's credential."""


@dataclass
class Execution:
    assignment: Assignment
    loop: asyncio.AbstractEventLoop | None = None
    task: asyncio.Task | None = None
    cancelled: bool = False
    lock: threading.Lock = field(default_factory=threading.Lock)
    redactor: Redactor = field(default_factory=Redactor)

    def cancel(self) -> None:
        with self.lock:
            self.cancelled = True
            if self.loop is not None and self.task is not None and not self.loop.is_closed():
                self.loop.call_soon_threadsafe(self.task.cancel)


class Agent:
    def __init__(
        self,
        client: WorkerClient,
        *,
        name: str,
        slots: int,
        bundle_dir: str,
        heartbeat_interval: float = 5.0,
        poll_wait: int = 20,
    ):
        self.client, self.name, self.slots = client, name, slots
        self.heartbeat_interval, self.poll_wait = heartbeat_interval, poll_wait
        self.bundles = BundleCache(client, bundle_dir)
        self.shipper = LogShipper(client)
        self.active: dict[str, Execution] = {}
        self.pool = concurrent.futures.ThreadPoolExecutor(
            max_workers=slots + 2, thread_name_prefix="flyke-exec"
        )
        self.stop = asyncio.Event()
        self.draining = False
        self.revoked = False
        self._slot_freed = asyncio.Event()
        self._jobs: set[asyncio.Task] = set()
        self._poll: asyncio.Task | None = None
        self._loop: asyncio.AbstractEventLoop | None = None

    # ------------------------------------------------------------ lifecycle ----

    async def run(self) -> int:
        install_capture()
        loop = asyncio.get_running_loop()
        self._loop = loop
        for sig in (signal.SIGINT, signal.SIGTERM):
            try:
                loop.add_signal_handler(sig, self.request_drain)
            except (NotImplementedError, RuntimeError):
                pass
        log.info("worker %s ready (%d slots)", self.name, self.slots)
        shipper = asyncio.create_task(self.shipper.run(self.stop))
        hb = asyncio.create_task(self._heartbeat_loop())
        try:
            await self._poll_loop()
            if self._jobs:
                log.info("draining: waiting for %d running item(s)", len(self._jobs))
                await asyncio.gather(*self._jobs, return_exceptions=True)
        finally:
            hb.cancel()
            self.stop.set()
            await asyncio.gather(shipper, hb, return_exceptions=True)
            self.pool.shutdown(wait=False, cancel_futures=True)
        return 3 if self.revoked else 0

    def request_drain(self) -> None:
        if self.draining:
            log.warning("second interrupt: cancelling running work")
            for ex in list(self.active.values()):
                ex.cancel()
            self.stop.set()
        self.draining = True
        self._slot_freed.set()
        if self._poll is not None:
            self._poll.cancel()  # don't sit in a long-poll while draining
        log.info("drain requested: finishing running work, accepting nothing new")

    # ---------------------------------------------------------------- loops ----

    async def _heartbeat_loop(self) -> None:
        while not self.stop.is_set():
            try:
                resp = await self.client.heartbeat(self.slots - len(self.active), list(self.active))
                for lease in resp.get("cancelLeases", []) or []:
                    if (ex := self.active.get(lease)) is not None:
                        log.info(
                            "cancelling %s (lease %s) at the control plane's request",
                            ex.assignment.name or "step",
                            lease,
                        )
                        ex.cancel()
                if resp.get("drain") and not self.draining:
                    self.request_drain()
            except ProtocolError as e:
                if e.unauthenticated:
                    self._revoke()
                    return
                log.warning("heartbeat failed: %s", e)
            try:
                await asyncio.wait_for(self.stop.wait(), timeout=self.heartbeat_interval)
            except TimeoutError:
                pass

    async def _poll_loop(self) -> None:
        backoff = 0.5
        while not self.draining and not self.stop.is_set():
            free = self.slots - len(self.active)
            if free <= 0:
                self._slot_freed.clear()
                await self._slot_freed.wait()
                continue
            self._poll = asyncio.create_task(self.client.poll(free, self.poll_wait))
            try:
                assignments = await self._poll
                backoff = 0.5
            except asyncio.CancelledError:
                if self.draining or self.stop.is_set():
                    break
                raise
            except ProtocolError as e:
                if e.unauthenticated:
                    self._revoke()
                    return
                log.warning("poll failed: %s (retrying in %.1fs)", e, backoff)
                await asyncio.sleep(backoff)
                backoff = min(backoff * 2, 10)
                continue
            for a in assignments:
                ex = Execution(a)
                self.active[a.lease_id] = ex
                job = asyncio.create_task(self._handle(ex))
                self._jobs.add(job)
                job.add_done_callback(self._jobs.discard)

    def _revoke(self) -> None:
        log.error("worker credential rejected (worker removed from the control plane?); exiting")
        self.revoked = True
        self.draining = True
        for ex in list(self.active.values()):
            ex.cancel()
        self.stop.set()
        self._slot_freed.set()

    # ------------------------------------------------------------ execution ----

    async def _handle(self, ex: Execution) -> None:
        a = ex.assignment
        try:
            if a.is_step:
                await self._run_step(ex)
            else:
                await self._run_task(ex)
        except Exception:  # noqa: BLE001 - never let one item kill the agent
            log.exception("unexpected error while handling lease %s", a.lease_id)
        finally:
            self.active.pop(a.lease_id, None)
            self._slot_freed.set()

    async def _root(self, a: Assignment) -> str:
        if a.bundle_digest:
            return await self.bundles.path(a.bundle_digest)
        return a.local_root

    async def _run_step(self, ex: Execution) -> None:
        a = ex.assignment
        sink = self.shipper.sink_for(a.run_id, a.work_id, 1)
        try:
            root = await self._root(a)
            ctx = RunContext(
                run_id=a.run_id, workflow=a.workflow, namespace=a.namespace, worker=self.name
            )

            def step() -> Any:
                defn = load(root, a.entrypoint)
                return replay_step(defn, a.inputs, a.history or [], ctx, sink)

            result = await asyncio.get_running_loop().run_in_executor(self.pool, step)
        except Exception as e:  # noqa: BLE001 - import errors, bundle errors…
            await self.client.fail_step(a.lease_id, Failure.from_exception(e))
            return
        await self.shipper.flush()  # logs land before the outcome they explain
        if result.failure is not None:
            await self.client.fail_step(a.lease_id, result.failure)
        else:
            await self.client.complete_step(a.lease_id, result.commands)

    async def _run_task(self, ex: Execution) -> None:
        a = ex.assignment
        if not await self.client.ack(a.lease_id):
            log.info("lease %s no longer valid; skipping %s", a.lease_id, a.name)
            return
        sink = self.shipper.sink_for(a.run_id, a.work_id, a.attempt, redact=ex.redactor)
        try:
            root = await self._root(a)
            result = await asyncio.get_running_loop().run_in_executor(
                self.pool, self._execute_in_thread, ex, root, sink
            )
        except asyncio.CancelledError:
            raise
        except BaseException as e:  # noqa: BLE001 - user code may raise anything
            if ex.cancelled:
                return  # the control plane already moved on
            if isinstance(e, (asyncio.CancelledError, concurrent.futures.CancelledError)):
                e = TimeoutError(f"task {a.name} exceeded its timeout")
            f = Failure.from_exception(e)
            f.message, f.stack = ex.redactor(f.message), ex.redactor(f.stack)
            await self.client.fail_task(a.lease_id, f)
            return
        finally:
            await self.shipper.flush()
            self.shipper.forget(a.work_id, a.attempt)
        if ex.cancelled:
            return
        try:
            payload = dumps(result)
        except TypeError as e:
            await self.client.fail_task(a.lease_id, Failure.from_exception(e))
            return
        await self.client.complete_task(a.lease_id, payload)

    def _secret_resolver(self, ex: Execution):
        """Resolver called from the task's thread; the RPC runs on the agent's loop."""
        main = self._loop
        lease = ex.assignment.lease_id

        def resolve(key: str) -> tuple[str, bool] | None:
            if main is None:
                raise RuntimeError("worker event loop is not running")
            found = asyncio.run_coroutine_threadsafe(
                self.client.get_secret(lease, key), main
            ).result(timeout=30)
            if found is not None and found[1]:
                ex.redactor.add(found[0])
            return found

        return resolve

    def _execute_in_thread(self, ex: Execution, root: str, sink: Any) -> Any:
        """Runs in a worker thread with its own event loop (isolates blocking code)."""
        a = ex.assignment
        defn = load(root, a.entrypoint)
        ctx = RunContext(
            run_id=a.run_id,
            workflow=a.workflow,
            namespace=a.namespace,
            task=a.name,
            attempt=a.attempt,
            idempotency_key=a.idempotency_key,
            worker=self.name,
        )
        coro = execute_task(
            defn,
            a.inputs,
            a.name,
            a.args or [],
            ctx,
            sink,
            compensation=a.compensation,
            compensated_result=a.compensated_result,
            secrets=self._secret_resolver(ex),
        )
        timeout = None
        if a.deadline is not None:
            timeout = max(0.0, (a.deadline - datetime.now(UTC)).total_seconds())
        loop = asyncio.new_event_loop()
        try:
            with ex.lock:
                if ex.cancelled:
                    coro.close()
                    raise asyncio.CancelledError()
                ex.loop = loop
                ex.task = loop.create_task(
                    asyncio.wait_for(coro, timeout) if timeout is not None else coro
                )
            with capture_into(sink):
                return loop.run_until_complete(ex.task)
        finally:
            with ex.lock:
                ex.loop = None
            loop.close()
