"""Secrets and variables, read at run time from the control plane.

    from flyke import secrets

    @task()
    async def sync(self) -> None:
        user = secrets.get("infoblox_username")      # a plain variable
        password = secrets["infoblox_password"]      # a sensitive secret
        region = secrets.get("region", "eu-west-1")  # with a default

Secrets are only readable inside tasks (and compensations): the workflow body
is replayed and must stay deterministic, so pass secrets to tasks by key, not
by value. A Private secret of the run's namespace wins over a Shared one.

Outside a Flyke worker (plain unit tests, scripts) the value is read from the
environment variable of the same name, so tests can set ``os.environ``.
"""

from __future__ import annotations

import os
from collections.abc import Callable
from typing import Any

from . import context
from .errors import ContextError, FlykeError

# resolver(key) -> (value, sensitive), or None when no such secret exists.
SecretResolver = Callable[[str], tuple[str, bool] | None]

_MISSING: Any = object()


class SecretNotFound(FlykeError, KeyError):
    """No secret with this key is visible from the run's namespace."""

    def __init__(self, key: str):
        self.key = key
        super().__init__(f"secret {key!r} not found (create it with `flyke secrets set {key} ...`)")

    def __str__(self) -> str:  # KeyError would repr() the message
        return self.args[0]


class SecretCache:
    """Per-attempt cache in front of the worker's resolver."""

    def __init__(self, resolver: SecretResolver | None):
        self._resolver = resolver
        self._values: dict[str, tuple[str, bool] | None] = {}

    def lookup(self, key: str) -> tuple[str, bool] | None:
        if key not in self._values:
            if self._resolver is None:
                raise ContextError("this worker does not support secrets")
            self._values[key] = self._resolver(key)
        return self._values[key]


class Secrets:
    """Mapping-like accessor exposed as ``flyke.secrets``."""

    def get(self, key: str, default: Any = _MISSING) -> Any:
        """Return the secret's value, else ``default`` (raises if no default)."""
        found = self._lookup(key)
        if found is not None:
            return found
        if default is not _MISSING:
            return default
        raise SecretNotFound(key)

    def __getitem__(self, key: str) -> str:
        return self.get(key)

    def __contains__(self, key: object) -> bool:
        return isinstance(key, str) and self._lookup(key) is not None

    def _lookup(self, key: str) -> str | None:
        rt = context.current()
        if rt is None:
            return os.environ.get(key)
        cache = getattr(rt, "secrets", None)
        if getattr(rt, "kind", "") != "task" or cache is None:
            raise ContextError(
                f"secrets.get({key!r}) is only allowed inside a @task: the workflow body is replayed and "
                "must be deterministic. Read the secret in the task that needs it."
            )
        found = cache.lookup(key)
        return found[0] if found is not None else None

    def __repr__(self) -> str:
        return "<flyke.secrets>"


secrets = Secrets()
