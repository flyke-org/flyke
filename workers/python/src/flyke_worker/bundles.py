"""Code bundle cache: fetch by digest, verify, extract safely."""

from __future__ import annotations

import asyncio
import hashlib
import io
import os
import shutil
import tarfile

from .protocol import WorkerClient


class BundleCache:
    def __init__(self, client: WorkerClient, directory: str):
        self.client, self.dir = client, directory
        self._locks: dict[str, asyncio.Lock] = {}
        os.makedirs(directory, exist_ok=True)

    async def path(self, digest: str) -> str:
        if not digest.startswith("sha256:") or len(digest) != 71:
            raise ValueError(f"invalid bundle digest {digest!r}")
        target = os.path.join(self.dir, digest[7:])
        if os.path.isdir(target):
            return target
        lock = self._locks.setdefault(digest, asyncio.Lock())
        async with lock:
            if os.path.isdir(target):
                return target
            data = await self.client.fetch_bundle(digest)
            actual = "sha256:" + hashlib.sha256(data).hexdigest()
            if actual != digest:
                raise ValueError(f"bundle digest mismatch: expected {digest}, got {actual}")
            tmp = target + ".partial"
            shutil.rmtree(tmp, ignore_errors=True)
            await asyncio.to_thread(_extract, data, tmp)
            os.replace(tmp, target)
        return target


def _extract(data: bytes, dest: str) -> None:
    os.makedirs(dest)
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as tar:
        if hasattr(tarfile, "data_filter"):
            tar.extractall(dest, filter="data")
            return
        root = os.path.realpath(dest)
        for m in tar.getmembers():
            p = os.path.realpath(os.path.join(dest, m.name))
            if (
                not (p == root or p.startswith(root + os.sep))
                or m.issym()
                or m.islnk()
                or m.isdev()
            ):
                raise ValueError(f"unsafe path in bundle: {m.name}")
        tar.extractall(dest)  # noqa: S202 - members validated above
