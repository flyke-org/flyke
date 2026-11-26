# Build from the repository root:  docker build -f workers/python/Dockerfile .
FROM python:3.12-slim
ENV FLYKE_WORKER_HOME=/var/lib/flyke-worker PYTHONUNBUFFERED=1
COPY sdk /src/sdk
COPY workers/python /src/worker
RUN pip install --no-cache-dir /src/sdk /src/worker && useradd -r -m flyke && mkdir -p $FLYKE_WORKER_HOME && chown flyke $FLYKE_WORKER_HOME
USER flyke
VOLUME ["/var/lib/flyke-worker"]
ENTRYPOINT ["flyke-worker"]
CMD ["start"]
