"""A realistic infra workflow showing every knob. Side effects are simulated.

flyke run examples/deploy.py:DeployService --service=api -f
flyke run examples/deploy.py:DeployService --service=api --env=prod -f
"""

import asyncio
import random
from typing import Literal

from flyke import (
    AT_MOST_ONCE,
    EFFECTIVELY_ONCE,
    Concurrency,
    NonRetryableError,
    Resources,
    Retry,
    on,
    task,
    workflow,
)


@workflow(
    triggers=[on.event("git.pushed", where={"branch": "main"})],
    concurrency=Concurrency(key="{service}:{env}", limit=1, on_conflict="queue"),
    resources=Resources(cpu=1, memory="512Mi"),
    timeout="45m",
    labels={"team": "platform"},
)
class DeployService:
    """Build, migrate, provision in every region, smoke-test, and wait for approval before finishing a prod deploy."""

    service: str
    env: Literal["staging", "prod"] = "staging"
    regions: list[str] = ["eu-west-1", "us-east-1"]
    flaky: bool = False

    @task(
        retry=Retry(attempts=5, backoff="exp", delay="200ms", max_delay="2s", on=(ConnectionError,))
    )
    async def build(self) -> str:
        if self.flaky and random.random() < 0.6:
            raise ConnectionError("registry unreachable")
        await asyncio.sleep(0.2)
        tag = f"{self.service}:{random.randint(1000, 9999)}"
        self.log.info("image built", tag=tag, attempt=self.ctx.attempt)
        return tag

    @task(guarantee=AT_MOST_ONCE, resources="heavy", timeout="10m", on_unknown="pause")
    async def migrate_db(self, image: str) -> None:
        if self.service == "broken":
            raise NonRetryableError("migration 0042 conflicts with schema")
        self.log.info("migrating database", image=image)
        await asyncio.sleep(0.2)

    @task(guarantee=EFFECTIVELY_ONCE, idempotency_key="{service}-{env}-{image}-{region}")
    async def provision(self, image: str, region: str) -> str:
        self.log.info("provisioning", region=region, key=self.ctx.idempotency_key)
        await asyncio.sleep(0.3)
        return f"https://{self.service}.{region}.example.com"

    @provision.compensate
    async def deprovision(self, image: str, region: str, result: str) -> None:
        self.log.warning("rolling back", region=region, endpoint=result)

    @task
    async def smoke_test(self, endpoints: list[str]) -> None:
        if self.service == "unstable":
            raise RuntimeError("smoke test failed: 502 from load balancer")
        self.log.info("smoke tests passed", endpoints=len(endpoints))

    async def run(self) -> dict:
        image = await self.build()
        await self.migrate_db(image)

        endpoints = await asyncio.gather(
            *(self.provision(image, region) for region in self.regions)
        )
        await self.smoke_test(list(endpoints))

        if self.env == "prod":
            self.log.info(
                "waiting for approval",
                hint=f"flyke emit deploy.approved service={self.service} user=you",
            )
            approval = await self.wait_for(
                "deploy.approved", where={"service": self.service}, timeout="24h"
            )
            self.log.info("approved", by=approval.user)

        await self.emit("deploy.finished", service=self.service, image=image)
        return {"image": image, "endpoints": list(endpoints), "finished_at": self.now().isoformat()}
