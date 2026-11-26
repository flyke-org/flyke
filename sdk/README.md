# flyke (Python SDK)

Decorators and runtime for writing Flyke workflows in plain Python.
It has no runtime dependencies and knows nothing about the control plane or
worker transport: workers use `flyke.runtime` to replay workflow steps and
execute tasks.

```python
from flyke import workflow, task, Retry


@workflow
class Hello:
    name: str = "world"

    @task(retry=Retry(attempts=3))
    async def greet(self) -> str:
        self.log.info("greeting", name=self.name)
        return f"hello {self.name}"

    async def run(self) -> str:
        return await self.greet()
```

`python -m flyke manifest hello.py:Hello` prints the manifest the control plane registers.
