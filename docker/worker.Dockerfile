# Dev image for the Python worker. The SDK and worker are installed in editable
# mode, and the source is bind-mounted over /src at runtime, so code changes are
# picked up; watchfiles restarts the worker when they happen.
FROM python:3.12-slim
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
COPY sdk /src/sdk
COPY workers/python /src/workers/python
RUN pip install --no-cache-dir -e /src/sdk -e /src/workers/python watchfiles
# Third-party packages imported by the workflows in examples/ (bundles only ship your own code).
COPY examples/requirements.txt /tmp/examples-requirements.txt
RUN pip install --no-cache-dir -r /tmp/examples-requirements.txt
WORKDIR /src
