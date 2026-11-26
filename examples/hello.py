from flyke import Retry, task, workflow


# New verison of the hello workflow using the new API
@workflow
class Hello:
    name: str = "world"

    @task(retry=Retry(attempts=3))
    async def greet(self) -> str:
        self.log.info("greeting", name=self.name)
        return f"hello {self.name}"

    async def run(self) -> str:
        return await self.greet()
