"""Flyke: code-first, durable workflows for application developers.

from flyke import workflow, task, Retry, on

@workflow(triggers=[on.event("user.signed_up")])
class Onboard:
    email: str

    @task(retry=Retry(attempts=5))
    async def send_welcome(self) -> None: ...

    async def run(self) -> None:
        await self.send_welcome()
        await self.wait_for("user.verified", where={"email": self.email}, timeout="7d")
"""

from ._internal.context import RunContext
from ._internal.definition import task, workflow
from ._internal.errors import (
    ContextError,
    FlykeError,
    NonDeterminismError,
    NonRetryableError,
    TaskError,
    WaitTimeout,
)
from ._internal.options import (
    AT_LEAST_ONCE,
    AT_MOST_ONCE,
    EFFECTIVELY_ONCE,
    Concurrency,
    Guarantee,
    Resources,
    Retry,
    on,
)
from ._internal.replay import Event
from ._internal.secrets import SecretNotFound, secrets

__version__ = "0.1.0.dev0"

__all__ = [
    "AT_LEAST_ONCE",
    "AT_MOST_ONCE",
    "EFFECTIVELY_ONCE",
    "Concurrency",
    "ContextError",
    "Event",
    "FlykeError",
    "Guarantee",
    "NonDeterminismError",
    "NonRetryableError",
    "Resources",
    "Retry",
    "RunContext",
    "SecretNotFound",
    "TaskError",
    "WaitTimeout",
    "on",
    "secrets",
    "task",
    "workflow",
]
