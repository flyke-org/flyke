// Package sqlite implements ports.Store on SQLite (pure Go driver, no cgo).
// It is the default store for local development and single-node installs.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
)

//go:embed schema.sql
var schema string

type Store struct{ db *sql.DB }

// Open opens (and migrates) a database. path ":memory:" gives an ephemeral store.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)&_pragma=synchronous(NORMAL)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection serialises transactions: simple and correct for SQLite.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// addedColumns are columns introduced after a table was first created.
// CREATE TABLE IF NOT EXISTS leaves existing tables alone, so they are added
// here; every entry must be nullable or have a default.
var addedColumns = []struct{ table, column, ddl string }{
	{"runs", "retry_of", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "retry_plan", "TEXT"},
	{"tasks", "reused_from", "TEXT NOT NULL DEFAULT ''"},
	{"secrets", "sensitive", "INTEGER NOT NULL DEFAULT 0"},
	{"pools", "namespace", "TEXT NOT NULL DEFAULT ''"},
	{"workers", "namespace", "TEXT NOT NULL DEFAULT ''"},
	{"workflows", "activated_at", "INTEGER"},
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	for _, c := range addedColumns {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", c.table, c.column, c.ddl)); err != nil {
				return fmt.Errorf("add %s.%s: %w", c.table, c.column, err)
			}
		}
	}
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS runs_retry_of ON runs (retry_of)`,
		`CREATE INDEX IF NOT EXISTS runs_version ON runs (namespace, workflow, version)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return seedNamespaces(db)
}

// seedNamespaces makes namespaces first-class on databases created before
// multi-tenancy: "default" always exists, and every namespace already in use
// becomes a row. Those keep access to shared workers and to the bundles their
// workflows reference, so nothing that ran before stops running.
func seedNamespaces(db *sql.DB) error {
	now := time.Now().UTC().UnixNano()
	for _, q := range []string{
		`INSERT OR IGNORE INTO namespaces (name, shared_workers, created_at) VALUES ('default', 1, ?1)`,
		`INSERT OR IGNORE INTO namespaces (name, shared_workers, created_at)
			SELECT namespace, 1, ?1 FROM workflows WHERE namespace != ''
			UNION SELECT namespace, 1, ?1 FROM runs WHERE namespace != ''
			UNION SELECT namespace, 1, ?1 FROM secrets WHERE namespace != ''`,
		`INSERT OR IGNORE INTO bundle_owners (namespace, digest, created_at)
			SELECT DISTINCT namespace, json_extract(source, '$.bundle_digest'), ?1 FROM workflows
			WHERE COALESCE(json_extract(source, '$.bundle_digest'), '') != ''`,
	} {
		if _, err := db.Exec(q, now); err != nil {
			return fmt.Errorf("seed namespaces: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Tx(ctx context.Context, fn func(ports.Repos) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&repos{q: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) Read(ctx context.Context, fn func(ports.Repos) error) error {
	return fn(&repos{q: s.db})
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type repos struct{ q querier }

func (r *repos) Workflows() ports.WorkflowRepo   { return workflowRepo{r.q} }
func (r *repos) Runs() ports.RunRepo             { return runRepo{r.q} }
func (r *repos) History() ports.HistoryRepo      { return historyRepo{r.q} }
func (r *repos) Tasks() ports.TaskRepo           { return taskRepo{r.q} }
func (r *repos) Waits() ports.WaitRepo           { return waitRepo{r.q} }
func (r *repos) Events() ports.EventRepo         { return eventRepo{r.q} }
func (r *repos) Workers() ports.WorkerRepo       { return workerRepo{r.q} }
func (r *repos) Pools() ports.PoolRepo           { return poolRepo{r.q} }
func (r *repos) JoinTokens() ports.JoinTokenRepo { return tokenRepo{r.q} }
func (r *repos) Logs() ports.LogRepo             { return logRepo{r.q} }
func (r *repos) Schedules() ports.ScheduleRepo   { return scheduleRepo{r.q} }
func (r *repos) Secrets() ports.SecretRepo       { return secretRepo{r.q} }
func (r *repos) Namespaces() ports.NamespaceRepo { return namespaceRepo{r.q} }
func (r *repos) AccessTokens() ports.AccessTokenRepo {
	return accessTokenRepo{r.q}
}
func (r *repos) Bundles() ports.BundleRepo { return bundleRepo{r.q} }

// ------------------------------------------------------------- helpers ----

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ErrNotFound
	}
	return err
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// rawOrNull stores raw JSON, keeping SQL NULL for empty values.
func rawOrNull(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func fromRaw(ns sql.NullString) json.RawMessage {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	return json.RawMessage(ns.String)
}

func fromJSON[T any](s sql.NullString, out *T) {
	if s.Valid && s.String != "" && s.String != "null" {
		_ = json.Unmarshal([]byte(s.String), out)
	}
}

func ns(t time.Time) int64 { return t.UTC().UnixNano() }

func nsPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().UnixNano()
}

func tFrom(n int64) time.Time { return time.Unix(0, n).UTC() }

func tPtr(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := tFrom(n.Int64)
	return &t
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
