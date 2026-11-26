-- Flyke control plane schema (SQLite). Times are UTC unix nanoseconds.

CREATE TABLE IF NOT EXISTS workflows (
    namespace   TEXT NOT NULL,
    name        TEXT NOT NULL,
    version     TEXT NOT NULL,
    manifest    TEXT NOT NULL,
    source      TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_seq INTEGER NOT NULL,
    PRIMARY KEY (namespace, name, version)
);

CREATE TABLE IF NOT EXISTS runs (
    id              TEXT PRIMARY KEY,
    namespace       TEXT NOT NULL,
    workflow        TEXT NOT NULL,
    version         TEXT NOT NULL,
    inputs          TEXT NOT NULL,
    labels          TEXT NOT NULL,
    phase           TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    message         TEXT NOT NULL DEFAULT '',
    concurrency_key TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    triggered_by    TEXT NOT NULL DEFAULT '',
    result          TEXT,
    failure         TEXT,
    needs_step      INTEGER NOT NULL DEFAULT 0,
    terminal_cause  TEXT NOT NULL DEFAULT '',
    compensation    TEXT NOT NULL DEFAULT '',
    history_length  INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    started_at      INTEGER,
    finished_at     INTEGER,
    deadline_at     INTEGER,
    rv              INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS runs_phase ON runs (phase, created_at);
CREATE INDEX IF NOT EXISTS runs_ns ON runs (namespace, created_at);
CREATE INDEX IF NOT EXISTS runs_ckey ON runs (namespace, workflow, concurrency_key);
CREATE INDEX IF NOT EXISTS runs_idem ON runs (namespace, workflow, idempotency_key);

CREATE TABLE IF NOT EXISTS history (
    run_id  TEXT NOT NULL,
    id      INTEGER NOT NULL,
    type    TEXT NOT NULL,
    time    INTEGER NOT NULL,
    ref     INTEGER NOT NULL DEFAULT 0,
    name    TEXT NOT NULL DEFAULT '',
    payload TEXT,
    failure TEXT,
    PRIMARY KEY (run_id, id)
);

CREATE TABLE IF NOT EXISTS tasks (
    id                  TEXT PRIMARY KEY,
    run_id              TEXT NOT NULL,
    namespace           TEXT NOT NULL,
    kind                TEXT NOT NULL,
    ref                 INTEGER NOT NULL,
    name                TEXT NOT NULL,
    compensated_task    TEXT NOT NULL DEFAULT '',
    args                TEXT,
    result              TEXT,
    attempt             INTEGER NOT NULL,
    phase               TEXT NOT NULL,
    worker              TEXT NOT NULL DEFAULT '',
    lease_id            TEXT NOT NULL DEFAULT '',
    lease_expires_at    INTEGER,
    not_before          INTEGER NOT NULL,
    deadline            INTEGER,
    idempotency_key     TEXT NOT NULL DEFAULT '',
    guarantee           TEXT NOT NULL DEFAULT '',
    requests            TEXT NOT NULL DEFAULT '{}',
    pool                TEXT NOT NULL DEFAULT '',
    step_history_length INTEGER NOT NULL DEFAULT 0,
    failure             TEXT,
    created_at          INTEGER NOT NULL,
    started_at          INTEGER,
    finished_at         INTEGER
);
CREATE INDEX IF NOT EXISTS tasks_pending ON tasks (phase, not_before);
CREATE INDEX IF NOT EXISTS tasks_run ON tasks (run_id);
CREATE INDEX IF NOT EXISTS tasks_lease ON tasks (lease_id);
CREATE INDEX IF NOT EXISTS tasks_worker ON tasks (worker, phase);
CREATE INDEX IF NOT EXISTS tasks_idem ON tasks (namespace, idempotency_key, phase);

CREATE TABLE IF NOT EXISTS waits (
    run_id     TEXT NOT NULL,
    ref        INTEGER NOT NULL,
    namespace  TEXT NOT NULL,
    kind       TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    where_json TEXT NOT NULL DEFAULT '{}',
    deadline   INTEGER,
    done       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, ref)
);
CREATE INDEX IF NOT EXISTS waits_open ON waits (done, namespace, name);
CREATE INDEX IF NOT EXISTS waits_due ON waits (done, deadline);

CREATE TABLE IF NOT EXISTS events (
    id         TEXT PRIMARY KEY,
    namespace  TEXT NOT NULL,
    name       TEXT NOT NULL,
    payload    TEXT NOT NULL,
    source     TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS events_name ON events (namespace, name, created_at);

CREATE TABLE IF NOT EXISTS workers (
    name           TEXT PRIMARY KEY,
    pool           TEXT NOT NULL,
    hostname       TEXT NOT NULL,
    labels         TEXT NOT NULL,
    runtimes       TEXT NOT NULL,
    slots          INTEGER NOT NULL,
    free_slots     INTEGER NOT NULL,
    capacity       TEXT NOT NULL,
    agent_version  TEXT NOT NULL DEFAULT '',
    phase          TEXT NOT NULL,
    unschedulable  INTEGER NOT NULL DEFAULT 0,
    draining       INTEGER NOT NULL DEFAULT 0,
    credential_id  TEXT NOT NULL UNIQUE,
    credential_sum TEXT NOT NULL,
    last_heartbeat INTEGER NOT NULL,
    created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pools (
    name           TEXT PRIMARY KEY,
    selector       TEXT NOT NULL,
    max_concurrent INTEGER NOT NULL DEFAULT 0,
    profiles       TEXT NOT NULL,
    created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS join_tokens (
    id             TEXT PRIMARY KEY,
    secret_hash    TEXT NOT NULL,
    pool           TEXT NOT NULL,
    expires_at     INTEGER NOT NULL,
    max_uses       INTEGER NOT NULL,
    uses           INTEGER NOT NULL,
    allowed_labels TEXT,
    created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS logs (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace TEXT NOT NULL,
    run_id    TEXT NOT NULL,
    work_id   TEXT NOT NULL DEFAULT '',
    task      TEXT NOT NULL DEFAULT '',
    ref       INTEGER NOT NULL DEFAULT 0,
    attempt   INTEGER NOT NULL DEFAULT 0,
    worker    TEXT NOT NULL DEFAULT '',
    seq       INTEGER NOT NULL DEFAULT 0,
    time      INTEGER NOT NULL,
    level     TEXT NOT NULL,
    source    TEXT NOT NULL,
    stream    TEXT NOT NULL DEFAULT '',
    message   TEXT NOT NULL,
    fields    TEXT
);
CREATE INDEX IF NOT EXISTS logs_run ON logs (run_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS logs_dedup ON logs (work_id, attempt, seq) WHERE source != 'system';

CREATE TABLE IF NOT EXISTS trigger_state (
    key        TEXT PRIMARY KEY,
    last_fired INTEGER NOT NULL
);
