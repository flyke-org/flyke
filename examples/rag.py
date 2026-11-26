import asyncio

from flyke import AT_LEAST_ONCE, EFFECTIVELY_ONCE, Retry, secrets, task, workflow


@workflow()
class RAG:
    """A RAG workflow"""

    document: str
    chunking_strategy: str = "simple"
    chunk_size: int = 2

    @task(guarantee=AT_LEAST_ONCE, retry=Retry(attempts=5, backoff="exp", delay="200ms"))
    async def load_document(self) -> str:
        # Simulate loading a document from a database or external source.
        # Secrets are read inside tasks; the defaults keep the example runnable.
        username = secrets.get("infoblox_username", "anonymous")
        password = secrets.get("infoblox_password", "")
        self.log.info("connecting", username=username, authenticated=bool(password))
        await asyncio.sleep(0.1)
        self.log.info("document loaded", document=self.document)
        return self.document

    @task(guarantee=AT_LEAST_ONCE)
    async def chunk_document(self, document: str, chunk_size: int) -> str:
        # Simulate document chunking logic
        await asyncio.sleep(0.1)
        return document.split()[:chunk_size]

    @task(guarantee=AT_LEAST_ONCE)
    async def another_chunk_document_parallel(self, document: str, chunk_size: int) -> str:
        # Simulate document chunking logic
        await asyncio.sleep(0.1)
        return document.split()[:chunk_size]

    @task(guarantee=EFFECTIVELY_ONCE, idempotency_key="welcome-email-document-{document}")
    async def embed_document(self, chunked_document: list) -> str:
        # Pass self.ctx.idempotency_key to your email provider so it deduplicates too.
        for chank in chunked_document:
            await asyncio.sleep(0.1)
            self.log.info("chunk embedded", chunk=chank, key=self.ctx.idempotency_key)

    async def run(self) -> dict:
        document = await self.load_document()
        _ = await self.another_chunk_document_parallel(document, self.chunk_size)
        chunked_document = await self.chunk_document(document, self.chunk_size)
        await self.embed_document(chunked_document)
