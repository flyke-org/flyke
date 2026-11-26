package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/event"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/schedule"
	"github.com/flyke-org/flyke/flyke/internal/domain/secret"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/tenant"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

// ------------------------------------------------------------ workflows ----

type workflowRepo struct{ q querier }

func (r workflowRepo) Put(ctx context.Context, wf workflow.Workflow) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO workflows (namespace, name, version, manifest, source, created_at, updated_seq, activated_at)
		VALUES (?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(updated_seq), 0) + 1 FROM workflows), ?)
		ON CONFLICT (namespace, name, version) DO UPDATE SET
			updated_seq = excluded.updated_seq, activated_at = excluded.activated_at`,
		wf.Namespace, wf.Name, wf.Version, toJSON(wf.Manifest), toJSON(wf.Source), ns(wf.CreatedAt), nsPtr(timeOrNil(wf.ActivatedAt)))
	return err
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

const wfCols = `namespace, name, version, manifest, source, created_at, COALESCE(activated_at, created_at)`

func scanWorkflow(sc interface{ Scan(...any) error }) (workflow.Workflow, error) {
	var wf workflow.Workflow
	var m, s string
	var created, activated int64
	if err := sc.Scan(&wf.Namespace, &wf.Name, &wf.Version, &m, &s, &created, &activated); err != nil {
		return wf, notFound(err)
	}
	if err := json.Unmarshal([]byte(m), &wf.Manifest); err != nil {
		return wf, err
	}
	if err := json.Unmarshal([]byte(s), &wf.Source); err != nil {
		return wf, err
	}
	wf.CreatedAt, wf.ActivatedAt = tFrom(created), tFrom(activated)
	return wf, nil
}

func (r workflowRepo) Get(ctx context.Context, nsp, name, version string) (workflow.Workflow, error) {
	return scanWorkflow(r.q.QueryRowContext(ctx, `SELECT `+wfCols+` FROM workflows WHERE namespace=? AND name=? AND version=?`, nsp, name, version))
}

func (r workflowRepo) GetLatest(ctx context.Context, nsp, name string) (workflow.Workflow, error) {
	return scanWorkflow(r.q.QueryRowContext(ctx, `SELECT `+wfCols+` FROM workflows WHERE namespace=? AND name=? ORDER BY updated_seq DESC LIMIT 1`, nsp, name))
}

func (r workflowRepo) ListLatest(ctx context.Context, nsp string) ([]workflow.Workflow, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+wfCols+` FROM workflows w
		WHERE (? = '' OR namespace = ?) AND updated_seq = (
			SELECT MAX(updated_seq) FROM workflows x WHERE x.namespace = w.namespace AND x.name = w.name)
		ORDER BY namespace, name`, nsp, nsp)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []workflow.Workflow
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, wf)
	}
	return out, rows.Err()
}

func (r workflowRepo) ListVersions(ctx context.Context, nsp, name string) ([]workflow.Workflow, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+wfCols+` FROM workflows WHERE namespace=? AND name=? ORDER BY updated_seq DESC`, nsp, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []workflow.Workflow
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, wf)
	}
	return out, rows.Err()
}

// ----------------------------------------------------------------- runs ----

type runRepo struct{ q querier }

func (r runRepo) VersionStats(ctx context.Context, nsp, wf string) (map[string]*ports.VersionRunStats, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT version, phase, COUNT(*), MIN(created_at), MAX(created_at) FROM runs
		WHERE namespace=? AND workflow=? GROUP BY version, phase`, nsp, wf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*ports.VersionRunStats{}
	for rows.Next() {
		var version, phase string
		var n int
		var first, last int64
		if err := rows.Scan(&version, &phase, &n, &first, &last); err != nil {
			return nil, err
		}
		st := out[version]
		if st == nil {
			st = &ports.VersionRunStats{Phases: map[run.Phase]int{}}
			out[version] = st
		}
		st.Total += n
		st.Phases[run.Phase(phase)] += n
		if f := tFrom(first); st.FirstRunAt == nil || f.Before(*st.FirstRunAt) {
			st.FirstRunAt = &f
		}
		if l := tFrom(last); st.LastRunAt == nil || l.After(*st.LastRunAt) {
			st.LastRunAt = &l
		}
	}
	return out, rows.Err()
}

const runCols = `id, namespace, workflow, version, inputs, labels, phase, reason, message,
	concurrency_key, idempotency_key, triggered_by, result, failure, needs_step, terminal_cause,
	compensation, history_length, created_at, started_at, finished_at, deadline_at, rv, retry_of, retry_plan`

func scanRun(sc interface{ Scan(...any) error }) (*run.Run, error) {
	var r run.Run
	var inputs, labels string
	var result, failure sql.NullString
	var needs int
	var phase, cause string
	var created int64
	var started, finished, deadline sql.NullInt64
	var retryOf string
	var retryPlan sql.NullString
	err := sc.Scan(&r.ID, &r.Namespace, &r.Workflow, &r.Version, &inputs, &labels, &phase, &r.Reason, &r.Message,
		&r.ConcurrencyKey, &r.IdempotencyKey, &r.TriggeredBy, &result, &failure, &needs, &cause,
		&r.Compensation, &r.HistoryLength, &created, &started, &finished, &deadline, &r.ResourceVersion,
		&retryOf, &retryPlan)
	if err != nil {
		return nil, notFound(err)
	}
	if retryOf != "" {
		r.Retry = &run.RetryPlan{}
		fromJSON(retryPlan, r.Retry)
		r.Retry.Of = retryOf
	}
	r.Inputs = json.RawMessage(inputs)
	_ = json.Unmarshal([]byte(labels), &r.Labels)
	r.Phase, r.TerminalCause = run.Phase(phase), run.Phase(cause)
	r.Result = fromRaw(result)
	if failure.Valid {
		r.Failure = &workflow.Failure{}
		fromJSON(failure, r.Failure)
	}
	r.NeedsStep = needs == 1
	r.CreatedAt = tFrom(created)
	r.StartedAt, r.FinishedAt, r.DeadlineAt = tPtr(started), tPtr(finished), tPtr(deadline)
	return &r, nil
}

func failureOrNull(f *workflow.Failure) any {
	if f == nil {
		return nil
	}
	return toJSON(f)
}

func (r runRepo) Create(ctx context.Context, x *run.Run) error {
	x.ResourceVersion = 1
	var retryOf string
	var retryPlan any
	if x.Retry != nil {
		retryOf, retryPlan = x.Retry.Of, toJSON(x.Retry)
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO runs (`+runCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		x.ID, x.Namespace, x.Workflow, x.Version, string(x.Inputs), toJSON(x.Labels), string(x.Phase), x.Reason, x.Message,
		x.ConcurrencyKey, x.IdempotencyKey, x.TriggeredBy, rawOrNull(x.Result), failureOrNull(x.Failure), b2i(x.NeedsStep),
		string(x.TerminalCause), x.Compensation, x.HistoryLength, ns(x.CreatedAt), nsPtr(x.StartedAt), nsPtr(x.FinishedAt),
		nsPtr(x.DeadlineAt), x.ResourceVersion, retryOf, retryPlan)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ports.ErrAlreadyExists
	}
	return err
}

func (r runRepo) Get(ctx context.Context, nsp, id string) (*run.Run, error) {
	return scanRun(r.q.QueryRowContext(ctx, `SELECT `+runCols+` FROM runs WHERE namespace=? AND id=?`, nsp, id))
}

func (r runRepo) GetByID(ctx context.Context, id string) (*run.Run, error) {
	return scanRun(r.q.QueryRowContext(ctx, `SELECT `+runCols+` FROM runs WHERE id=?`, id))
}

func (r runRepo) Update(ctx context.Context, x *run.Run) error {
	res, err := r.q.ExecContext(ctx, `UPDATE runs SET phase=?, reason=?, message=?, result=?, failure=?, needs_step=?,
		terminal_cause=?, compensation=?, history_length=?, started_at=?, finished_at=?, deadline_at=?, labels=?, rv=rv+1
		WHERE id=? AND rv=?`,
		string(x.Phase), x.Reason, x.Message, rawOrNull(x.Result), failureOrNull(x.Failure), b2i(x.NeedsStep),
		string(x.TerminalCause), x.Compensation, x.HistoryLength, nsPtr(x.StartedAt), nsPtr(x.FinishedAt),
		nsPtr(x.DeadlineAt), toJSON(x.Labels), x.ID, x.ResourceVersion)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: run %s at resourceVersion %d", ports.ErrConflict, x.ID, x.ResourceVersion)
	}
	x.ResourceVersion++
	return nil
}

func (r runRepo) query(ctx context.Context, where string, args ...any) ([]*run.Run, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+runCols+` FROM runs WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*run.Run
	for rows.Next() {
		x, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (r runRepo) List(ctx context.Context, f ports.RunFilter) ([]*run.Run, error) {
	conds := []string{"1=1"}
	var args []any
	if f.Namespace != "" {
		conds, args = append(conds, "namespace=?"), append(args, f.Namespace)
	}
	if f.Workflow != "" {
		conds, args = append(conds, "workflow=?"), append(args, f.Workflow)
	}
	if len(f.Phases) > 0 {
		ph := make([]string, len(f.Phases))
		for i, p := range f.Phases {
			ph[i] = "?"
			args = append(args, string(p))
		}
		conds = append(conds, "phase IN ("+strings.Join(ph, ",")+")")
	}
	if f.Version != "" {
		conds, args = append(conds, "version=?"), append(args, f.Version)
	}
	if f.RetryOf != "" {
		conds, args = append(conds, "retry_of=?"), append(args, f.RetryOf)
	}
	if f.NeedsStep != nil {
		conds, args = append(conds, "needs_step=?"), append(args, b2i(*f.NeedsStep))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)
	return r.query(ctx, strings.Join(conds, " AND ")+" ORDER BY created_at DESC LIMIT ?", args...)
}

func (r runRepo) FindByIdempotencyKey(ctx context.Context, nsp, wf, key string) (*run.Run, error) {
	return scanRun(r.q.QueryRowContext(ctx, `SELECT `+runCols+` FROM runs WHERE namespace=? AND workflow=? AND idempotency_key=? LIMIT 1`, nsp, wf, key))
}

var terminalPhases = `('Succeeded','Failed','Cancelled','TimedOut','Skipped')`

func (r runRepo) ListByConcurrencyKey(ctx context.Context, nsp, wf, key string) ([]*run.Run, error) {
	return r.query(ctx, `namespace=? AND workflow=? AND concurrency_key=? AND phase NOT IN `+terminalPhases+` ORDER BY created_at, id`, nsp, wf, key)
}

func (r runRepo) ListPastDeadline(ctx context.Context, now time.Time) ([]*run.Run, error) {
	return r.query(ctx, `deadline_at IS NOT NULL AND deadline_at < ? AND phase IN ('Running','Waiting','Paused')`, ns(now))
}

// -------------------------------------------------------------- history ----

type historyRepo struct{ q querier }

func (r historyRepo) Append(ctx context.Context, runID string, evs ...run.HistoryEvent) ([]run.HistoryEvent, error) {
	var last int
	if err := r.q.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM history WHERE run_id=?`, runID).Scan(&last); err != nil {
		return nil, err
	}
	out := make([]run.HistoryEvent, len(evs))
	for i, ev := range evs {
		last++
		ev.ID = last
		if _, err := r.q.ExecContext(ctx, `INSERT INTO history (run_id, id, type, time, ref, name, payload, failure) VALUES (?,?,?,?,?,?,?,?)`,
			runID, ev.ID, string(ev.Type), ns(ev.Time), ev.Ref, ev.Name, rawOrNull(ev.Payload), failureOrNull(ev.Failure)); err != nil {
			return nil, err
		}
		out[i] = ev
	}
	return out, nil
}

func (r historyRepo) List(ctx context.Context, runID string) ([]run.HistoryEvent, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, type, time, ref, name, payload, failure FROM history WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []run.HistoryEvent
	for rows.Next() {
		var ev run.HistoryEvent
		var typ string
		var t int64
		var payload, failure sql.NullString
		if err := rows.Scan(&ev.ID, &typ, &t, &ev.Ref, &ev.Name, &payload, &failure); err != nil {
			return nil, err
		}
		ev.Type, ev.Time, ev.Payload = run.EventType(typ), tFrom(t), fromRaw(payload)
		if failure.Valid {
			ev.Failure = &workflow.Failure{}
			fromJSON(failure, ev.Failure)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- tasks ----

type taskRepo struct{ q querier }

const taskCols = `id, run_id, namespace, kind, ref, name, compensated_task, args, result, attempt, phase, worker,
	lease_id, lease_expires_at, not_before, deadline, idempotency_key, guarantee, requests, pool,
	step_history_length, failure, created_at, started_at, finished_at, reused_from`

func scanTask(sc interface{ Scan(...any) error }) (*task.TaskRun, error) {
	var t task.TaskRun
	var kind, phase, guarantee, requests string
	var args, result, failure sql.NullString
	var leaseExp, deadline, started, finished sql.NullInt64
	var notBefore, created int64
	err := sc.Scan(&t.ID, &t.RunID, &t.Namespace, &kind, &t.Ref, &t.Name, &t.CompensatedTask, &args, &result, &t.Attempt,
		&phase, &t.Worker, &t.LeaseID, &leaseExp, &notBefore, &deadline, &t.IdempotencyKey, &guarantee, &requests, &t.Pool,
		&t.StepHistoryLength, &failure, &created, &started, &finished, &t.ReusedFrom)
	if err != nil {
		return nil, notFound(err)
	}
	t.Kind, t.Phase, t.Guarantee = task.Kind(kind), task.Phase(phase), workflow.Guarantee(guarantee)
	t.Args, t.Result = fromRaw(args), fromRaw(result)
	_ = json.Unmarshal([]byte(requests), &t.Requests)
	if failure.Valid {
		t.Failure = &workflow.Failure{}
		fromJSON(failure, t.Failure)
	}
	t.LeaseExpiresAt, t.Deadline = tPtr(leaseExp), tPtr(deadline)
	t.NotBefore, t.CreatedAt = tFrom(notBefore), tFrom(created)
	t.StartedAt, t.FinishedAt = tPtr(started), tPtr(finished)
	return &t, nil
}

func (r taskRepo) Create(ctx context.Context, t *task.TaskRun) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO tasks (`+taskCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.RunID, t.Namespace, string(t.Kind), t.Ref, t.Name, t.CompensatedTask, rawOrNull(t.Args), rawOrNull(t.Result),
		t.Attempt, string(t.Phase), t.Worker, t.LeaseID, nsPtr(t.LeaseExpiresAt), ns(t.NotBefore), nsPtr(t.Deadline),
		t.IdempotencyKey, string(t.Guarantee), toJSON(t.Requests), t.Pool, t.StepHistoryLength, failureOrNull(t.Failure),
		ns(t.CreatedAt), nsPtr(t.StartedAt), nsPtr(t.FinishedAt), t.ReusedFrom)
	return err
}

func (r taskRepo) Update(ctx context.Context, t *task.TaskRun) error {
	_, err := r.q.ExecContext(ctx, `UPDATE tasks SET args=?, result=?, attempt=?, phase=?, worker=?, lease_id=?,
		lease_expires_at=?, not_before=?, deadline=?, failure=?, started_at=?, finished_at=? WHERE id=?`,
		rawOrNull(t.Args), rawOrNull(t.Result), t.Attempt, string(t.Phase), t.Worker, t.LeaseID, nsPtr(t.LeaseExpiresAt),
		ns(t.NotBefore), nsPtr(t.Deadline), failureOrNull(t.Failure), nsPtr(t.StartedAt), nsPtr(t.FinishedAt), t.ID)
	return err
}

func (r taskRepo) one(ctx context.Context, where string, args ...any) (*task.TaskRun, error) {
	return scanTask(r.q.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE `+where, args...))
}

func (r taskRepo) many(ctx context.Context, where string, args ...any) ([]*task.TaskRun, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*task.TaskRun
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r taskRepo) Get(ctx context.Context, id string) (*task.TaskRun, error) {
	return r.one(ctx, `id=?`, id)
}

func (r taskRepo) GetByLease(ctx context.Context, lease string) (*task.TaskRun, error) {
	if lease == "" {
		return nil, ports.ErrNotFound
	}
	return r.one(ctx, `lease_id=?`, lease)
}

func (r taskRepo) ListByRun(ctx context.Context, runID string) ([]*task.TaskRun, error) {
	return r.many(ctx, `run_id=? ORDER BY created_at, id`, runID)
}

func (r taskRepo) ListPending(ctx context.Context, now time.Time, limit int, namespaces []string) ([]*task.TaskRun, error) {
	// Steps first: they unblock everything else of their run.
	const order = ` ORDER BY (kind='step') DESC, created_at LIMIT ?`
	if namespaces == nil {
		return r.many(ctx, `phase='Pending' AND not_before <= ?`+order, ns(now), limit)
	}
	if len(namespaces) == 0 {
		return nil, nil
	}
	args := []any{ns(now)}
	for _, n := range namespaces {
		args = append(args, n)
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(namespaces)), ",")
	return r.many(ctx, `phase='Pending' AND not_before <= ? AND namespace IN (`+in+`)`+order, append(args, limit)...)
}

func (r taskRepo) ListExpiredLeases(ctx context.Context, now time.Time) ([]*task.TaskRun, error) {
	return r.many(ctx, `phase IN ('Scheduled','Running') AND lease_expires_at IS NOT NULL AND lease_expires_at < ?`, ns(now))
}

func (r taskRepo) ListPastDeadline(ctx context.Context, now time.Time) ([]*task.TaskRun, error) {
	return r.many(ctx, `phase='Running' AND deadline IS NOT NULL AND deadline < ?`, ns(now))
}

func (r taskRepo) ListActiveByWorker(ctx context.Context, w string) ([]*task.TaskRun, error) {
	return r.many(ctx, `worker=? AND phase IN ('Scheduled','Running')`, w)
}

func (r taskRepo) FindSucceededByIdempotencyKey(ctx context.Context, nsp, key string) (*task.TaskRun, error) {
	return r.one(ctx, `namespace=? AND idempotency_key=? AND phase='Succeeded' AND kind='task' ORDER BY finished_at DESC LIMIT 1`, nsp, key)
}

func (r taskRepo) CountActiveInPool(ctx context.Context, pool string) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE pool=? AND phase IN ('Scheduled','Running')`, pool).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------- waits ----

type waitRepo struct{ q querier }

func (r waitRepo) Create(ctx context.Context, w run.Wait) error {
	_, err := r.q.ExecContext(ctx, `INSERT OR REPLACE INTO waits (run_id, ref, namespace, kind, name, where_json, deadline, done) VALUES (?,?,?,?,?,?,?,0)`,
		w.RunID, w.Ref, w.Namespace, w.Kind, w.Name, toJSON(w.Where), nsPtr(w.Deadline))
	return err
}

func (r waitRepo) list(ctx context.Context, where string, args ...any) ([]run.Wait, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT run_id, ref, namespace, kind, name, where_json, deadline, done FROM waits WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []run.Wait
	for rows.Next() {
		var w run.Wait
		var wh string
		var deadline sql.NullInt64
		var done int
		if err := rows.Scan(&w.RunID, &w.Ref, &w.Namespace, &w.Kind, &w.Name, &wh, &deadline, &done); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(wh), &w.Where)
		w.Deadline, w.Done = tPtr(deadline), done == 1
		out = append(out, w)
	}
	return out, rows.Err()
}

func (r waitRepo) ListOpenEvent(ctx context.Context, nsp, name string) ([]run.Wait, error) {
	return r.list(ctx, `done=0 AND kind='event' AND namespace=? AND name=? ORDER BY rowid`, nsp, name)
}

func (r waitRepo) ListDue(ctx context.Context, now time.Time) ([]run.Wait, error) {
	return r.list(ctx, `done=0 AND deadline IS NOT NULL AND deadline <= ?`, ns(now))
}

func (r waitRepo) MarkDone(ctx context.Context, runID string, ref int) error {
	_, err := r.q.ExecContext(ctx, `UPDATE waits SET done=1 WHERE run_id=? AND ref=?`, runID, ref)
	return err
}

func (r waitRepo) CloseAll(ctx context.Context, runID string) error {
	_, err := r.q.ExecContext(ctx, `UPDATE waits SET done=1 WHERE run_id=?`, runID)
	return err
}

func (r waitRepo) CountOpen(ctx context.Context, runID string) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM waits WHERE run_id=? AND done=0`, runID).Scan(&n)
	return n, err
}

// --------------------------------------------------------------- events ----

type eventRepo struct{ q querier }

func (r eventRepo) Create(ctx context.Context, e event.Event) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO events (id, namespace, name, payload, source, created_at) VALUES (?,?,?,?,?,?)`,
		e.ID, e.Namespace, e.Name, string(e.Payload), e.Source, ns(e.CreatedAt))
	return err
}

func (r eventRepo) List(ctx context.Context, nsp, name string, limit int) ([]event.Event, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.q.QueryContext(ctx, `SELECT id, namespace, name, payload, source, created_at FROM events
		WHERE namespace=? AND (?='' OR name=?) ORDER BY created_at DESC LIMIT ?`, nsp, name, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []event.Event
	for rows.Next() {
		var e event.Event
		var p string
		var c int64
		if err := rows.Scan(&e.ID, &e.Namespace, &e.Name, &p, &e.Source, &c); err != nil {
			return nil, err
		}
		e.Payload, e.CreatedAt = json.RawMessage(p), tFrom(c)
		out = append(out, e)
	}
	return out, rows.Err()
}

// -------------------------------------------------------------- workers ----

type workerRepo struct{ q querier }

const workerCols = `name, namespace, pool, hostname, labels, runtimes, slots, free_slots, capacity, agent_version, phase,
	unschedulable, draining, credential_id, credential_sum, last_heartbeat, created_at`

func scanWorker(sc interface{ Scan(...any) error }) (*worker.Worker, error) {
	var w worker.Worker
	var labels, runtimes, capacity, phase string
	var unsched, draining int
	var hb, created int64
	if err := sc.Scan(&w.Name, &w.Namespace, &w.Pool, &w.Hostname, &labels, &runtimes, &w.Slots, &w.FreeSlots, &capacity, &w.AgentVersion,
		&phase, &unsched, &draining, &w.CredentialID, &w.CredentialSum, &hb, &created); err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal([]byte(labels), &w.Labels)
	_ = json.Unmarshal([]byte(runtimes), &w.Runtimes)
	_ = json.Unmarshal([]byte(capacity), &w.Capacity)
	w.Phase, w.Unschedulable, w.Draining = worker.Phase(phase), unsched == 1, draining == 1
	w.LastHeartbeat, w.CreatedAt = tFrom(hb), tFrom(created)
	return &w, nil
}

func (r workerRepo) Create(ctx context.Context, w *worker.Worker) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO workers (`+workerCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		w.Name, w.Namespace, w.Pool, w.Hostname, toJSON(w.Labels), toJSON(w.Runtimes), w.Slots, w.FreeSlots, toJSON(w.Capacity),
		w.AgentVersion, string(w.Phase), b2i(w.Unschedulable), b2i(w.Draining), w.CredentialID, w.CredentialSum,
		ns(w.LastHeartbeat), ns(w.CreatedAt))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ports.ErrAlreadyExists
	}
	return err
}

func (r workerRepo) Get(ctx context.Context, name string) (*worker.Worker, error) {
	return scanWorker(r.q.QueryRowContext(ctx, `SELECT `+workerCols+` FROM workers WHERE name=?`, name))
}

func (r workerRepo) GetByCredentialID(ctx context.Context, id string) (*worker.Worker, error) {
	return scanWorker(r.q.QueryRowContext(ctx, `SELECT `+workerCols+` FROM workers WHERE credential_id=?`, id))
}

func (r workerRepo) Update(ctx context.Context, w *worker.Worker) error {
	_, err := r.q.ExecContext(ctx, `UPDATE workers SET labels=?, slots=?, free_slots=?, capacity=?, phase=?, unschedulable=?,
		draining=?, last_heartbeat=? WHERE name=?`,
		toJSON(w.Labels), w.Slots, w.FreeSlots, toJSON(w.Capacity), string(w.Phase), b2i(w.Unschedulable), b2i(w.Draining),
		ns(w.LastHeartbeat), w.Name)
	return err
}

func (r workerRepo) List(ctx context.Context) ([]*worker.Worker, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+workerCols+` FROM workers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*worker.Worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (r workerRepo) Delete(ctx context.Context, name string) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM workers WHERE name=?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ports.ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------- pools ----

type poolRepo struct{ q querier }

func scanPool(sc interface{ Scan(...any) error }) (*worker.Pool, error) {
	var p worker.Pool
	var sel, prof string
	var created int64
	if err := sc.Scan(&p.Name, &p.Namespace, &sel, &p.MaxConcurrentTasks, &prof, &created); err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal([]byte(sel), &p.Selector)
	_ = json.Unmarshal([]byte(prof), &p.ResourceProfiles)
	p.CreatedAt = tFrom(created)
	return &p, nil
}

func (r poolRepo) Put(ctx context.Context, p *worker.Pool) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO pools (name, namespace, selector, max_concurrent, profiles, created_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT (name) DO UPDATE SET selector=excluded.selector, max_concurrent=excluded.max_concurrent, profiles=excluded.profiles`,
		p.Name, p.Namespace, toJSON(p.Selector), p.MaxConcurrentTasks, toJSON(p.ResourceProfiles), ns(p.CreatedAt))
	return err
}

func (r poolRepo) Get(ctx context.Context, name string) (*worker.Pool, error) {
	return scanPool(r.q.QueryRowContext(ctx, `SELECT name, namespace, selector, max_concurrent, profiles, created_at FROM pools WHERE name=?`, name))
}

func (r poolRepo) List(ctx context.Context) ([]*worker.Pool, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT name, namespace, selector, max_concurrent, profiles, created_at FROM pools ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*worker.Pool
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r poolRepo) Delete(ctx context.Context, name string) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM pools WHERE name=?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ports.ErrNotFound
	}
	_, err = r.q.ExecContext(ctx, `DELETE FROM join_tokens WHERE pool=?`, name)
	return err
}

// ----------------------------------------------------------- join tokens ----

type tokenRepo struct{ q querier }

func scanToken(sc interface{ Scan(...any) error }) (worker.JoinToken, error) {
	var t worker.JoinToken
	var exp, created int64
	var allowed sql.NullString
	if err := sc.Scan(&t.ID, &t.SecretHash, &t.Pool, &exp, &t.MaxUses, &t.Uses, &allowed, &created); err != nil {
		return t, notFound(err)
	}
	fromJSON(allowed, &t.AllowedLabels)
	t.ExpiresAt, t.CreatedAt = tFrom(exp), tFrom(created)
	return t, nil
}

const tokenCols = `id, secret_hash, pool, expires_at, max_uses, uses, allowed_labels, created_at`

func (r tokenRepo) Create(ctx context.Context, t worker.JoinToken) error {
	var allowed any
	if t.AllowedLabels != nil {
		allowed = toJSON(t.AllowedLabels)
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO join_tokens (`+tokenCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		t.ID, t.SecretHash, t.Pool, ns(t.ExpiresAt), t.MaxUses, t.Uses, allowed, ns(t.CreatedAt))
	return err
}

func (r tokenRepo) Get(ctx context.Context, id string) (worker.JoinToken, error) {
	return scanToken(r.q.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM join_tokens WHERE id=?`, id))
}

func (r tokenRepo) Update(ctx context.Context, t worker.JoinToken) error {
	_, err := r.q.ExecContext(ctx, `UPDATE join_tokens SET uses=? WHERE id=?`, t.Uses, t.ID)
	return err
}

func (r tokenRepo) List(ctx context.Context, pool string) ([]worker.JoinToken, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+tokenCols+` FROM join_tokens WHERE (?='' OR pool=?) ORDER BY created_at`, pool, pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []worker.JoinToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r tokenRepo) Delete(ctx context.Context, id string) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM join_tokens WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ports.ErrNotFound
	}
	return nil
}

// ----------------------------------------------------------------- logs ----

type logRepo struct{ q querier }

func (r logRepo) Append(ctx context.Context, entries ...ports.LogEntry) (int, error) {
	n := 0
	for _, e := range entries {
		res, err := r.q.ExecContext(ctx, `INSERT OR IGNORE INTO logs (namespace, run_id, work_id, task, ref, attempt, worker, seq,
			time, level, source, stream, message, fields) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.Namespace, e.RunID, e.WorkID, e.Task, e.Ref, e.Attempt, e.Worker, e.Seq, ns(e.Time), e.Level, e.Source,
			e.Stream, e.Message, rawOrNull(e.Fields))
		if err != nil {
			return n, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	return n, nil
}

func (r logRepo) List(ctx context.Context, runID string, after int64, limit int) ([]ports.LogEntry, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := r.q.QueryContext(ctx, `SELECT id, namespace, run_id, work_id, task, ref, attempt, worker, seq, time, level,
		source, stream, message, fields FROM logs WHERE run_id=? AND id > ? ORDER BY id LIMIT ?`, runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ports.LogEntry
	for rows.Next() {
		var e ports.LogEntry
		var t int64
		var fields sql.NullString
		if err := rows.Scan(&e.ID, &e.Namespace, &e.RunID, &e.WorkID, &e.Task, &e.Ref, &e.Attempt, &e.Worker, &e.Seq, &t,
			&e.Level, &e.Source, &e.Stream, &e.Message, &fields); err != nil {
			return nil, err
		}
		e.Time, e.Fields = tFrom(t), fromRaw(fields)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------- schedules ----

type scheduleRepo struct{ q querier }

const scheduleCols = `namespace, name, workflow, cron, tz, inputs, missed, paused, declared,
	next_run_at, last_run_at, last_run_id, last_error, created_at, updated_at`

func scanSchedule(sc interface{ Scan(...any) error }) (*schedule.Schedule, error) {
	var s schedule.Schedule
	var inputs sql.NullString
	var paused, declared int
	var next, last sql.NullInt64
	var created, updated int64
	if err := sc.Scan(&s.Namespace, &s.Name, &s.Workflow, &s.Cron, &s.TZ, &inputs, &s.Missed, &paused, &declared,
		&next, &last, &s.LastRunID, &s.LastError, &created, &updated); err != nil {
		return nil, notFound(err)
	}
	fromJSON(inputs, &s.Inputs)
	s.Paused, s.Declared = paused == 1, declared == 1
	s.NextRunAt, s.LastRunAt = tPtr(next), tPtr(last)
	s.CreatedAt, s.UpdatedAt = tFrom(created), tFrom(updated)
	return &s, nil
}

func (r scheduleRepo) Put(ctx context.Context, s *schedule.Schedule) error {
	inputs := s.Inputs
	if inputs == nil {
		inputs = map[string]any{}
	}
	_, err := r.q.ExecContext(ctx, `INSERT OR REPLACE INTO schedules (`+scheduleCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.Namespace, s.Name, s.Workflow, s.Cron, s.TZ, toJSON(inputs), string(s.Missed), b2i(s.Paused), b2i(s.Declared),
		nsPtr(s.NextRunAt), nsPtr(s.LastRunAt), s.LastRunID, s.LastError, ns(s.CreatedAt), ns(s.UpdatedAt))
	return err
}

func (r scheduleRepo) Get(ctx context.Context, nsp, name string) (*schedule.Schedule, error) {
	return scanSchedule(r.q.QueryRowContext(ctx, `SELECT `+scheduleCols+` FROM schedules WHERE namespace=? AND name=?`, nsp, name))
}

func (r scheduleRepo) list(ctx context.Context, query string, args ...any) ([]*schedule.Schedule, error) {
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*schedule.Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r scheduleRepo) List(ctx context.Context, nsp, workflow string) ([]*schedule.Schedule, error) {
	return r.list(ctx, `SELECT `+scheduleCols+` FROM schedules WHERE namespace=?1 AND (?2 = '' OR workflow = ?2) ORDER BY name`, nsp, workflow)
}

func (r scheduleRepo) Delete(ctx context.Context, nsp, name string) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM schedules WHERE namespace=? AND name=?`, nsp, name)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ports.ErrNotFound
	}
	return nil
}

func (r scheduleRepo) ListDue(ctx context.Context, now time.Time, limit int) ([]*schedule.Schedule, error) {
	return r.list(ctx, `SELECT `+scheduleCols+` FROM schedules WHERE next_run_at IS NOT NULL AND next_run_at <= ?
		ORDER BY next_run_at LIMIT ?`, ns(now), limit)
}

func (r scheduleRepo) NextDue(ctx context.Context) (time.Time, error) {
	var next sql.NullInt64
	err := r.q.QueryRowContext(ctx, `SELECT MIN(next_run_at) FROM schedules WHERE next_run_at IS NOT NULL`).Scan(&next)
	if err != nil || !next.Valid {
		return time.Time{}, err
	}
	return tFrom(next.Int64), nil
}

// -------------------------------------------------------------- secrets ----

type secretRepo struct{ q querier }

const secretCols = `id, namespace, key, value, sensitive, type, created_at, updated_at`

func scanSecret(sc interface{ Scan(...any) error }) (secret.Secret, error) {
	var s secret.Secret
	var typ string
	var created, updated int64
	if err := sc.Scan(&s.ID, &s.Namespace, &s.Key, &s.Value, &s.Sensitive, &typ, &created, &updated); err != nil {
		return s, notFound(err)
	}
	s.Type = secret.SecretType(typ)
	s.CreatedAt, s.UpdatedAt = tFrom(created), tFrom(updated)
	return s, nil
}

func secretArgs(s secret.Secret) []any {
	return []any{s.ID, s.Namespace, s.Key, s.Value, s.Sensitive, string(s.Type), ns(s.CreatedAt), ns(s.UpdatedAt)}
}

func (r secretRepo) Create(ctx context.Context, s secret.Secret) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO secrets (`+secretCols+`) VALUES (?,?,?,?,?,?,?,?)`, secretArgs(s)...)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ports.ErrAlreadyExists
	}
	return err
}

func (r secretRepo) Put(ctx context.Context, s secret.Secret) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO secrets (`+secretCols+`) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT (namespace, key) DO UPDATE SET value=excluded.value, sensitive=excluded.sensitive,
			type=excluded.type, updated_at=excluded.updated_at`, secretArgs(s)...)
	return err
}

func (r secretRepo) Get(ctx context.Context, nsp, key string) (secret.Secret, error) {
	return scanSecret(r.q.QueryRowContext(ctx, `SELECT `+secretCols+` FROM secrets WHERE namespace=? AND key=?`, nsp, key))
}

func (r secretRepo) List(ctx context.Context, nsp string) ([]secret.Secret, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+secretCols+` FROM secrets WHERE namespace=? ORDER BY key`, nsp)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []secret.Secret
	for rows.Next() {
		s, err := scanSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r secretRepo) Delete(ctx context.Context, nsp, key string) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM secrets WHERE namespace=? AND key=?`, nsp, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ports.ErrNotFound
	}
	return nil
}

// ------------------------------------------------------------ namespaces ----

type namespaceRepo struct{ q querier }

func scanNamespace(sc interface{ Scan(...any) error }) (tenant.Namespace, error) {
	var n tenant.Namespace
	var shared int
	var created int64
	if err := sc.Scan(&n.Name, &n.Description, &shared, &created); err != nil {
		return n, notFound(err)
	}
	n.SharedWorkers, n.CreatedAt = shared == 1, tFrom(created)
	return n, nil
}

func (r namespaceRepo) Create(ctx context.Context, n tenant.Namespace) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO namespaces (name, description, shared_workers, created_at) VALUES (?,?,?,?)`,
		n.Name, n.Description, b2i(n.SharedWorkers), ns(n.CreatedAt))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ports.ErrAlreadyExists
	}
	return err
}

func (r namespaceRepo) Get(ctx context.Context, name string) (tenant.Namespace, error) {
	return scanNamespace(r.q.QueryRowContext(ctx, `SELECT name, description, shared_workers, created_at FROM namespaces WHERE name=?`, name))
}

func (r namespaceRepo) List(ctx context.Context) ([]tenant.Namespace, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT name, description, shared_workers, created_at FROM namespaces ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant.Namespace
	for rows.Next() {
		n, err := scanNamespace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (r namespaceRepo) Update(ctx context.Context, n tenant.Namespace) error {
	res, err := r.q.ExecContext(ctx, `UPDATE namespaces SET description=?, shared_workers=? WHERE name=?`,
		n.Description, b2i(n.SharedWorkers), n.Name)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ports.ErrNotFound
	}
	return nil
}

func (r namespaceRepo) Delete(ctx context.Context, name string) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM namespaces WHERE name=?`, name)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ports.ErrNotFound
	}
	for _, q := range []string{
		`DELETE FROM history WHERE run_id IN (SELECT id FROM runs WHERE namespace=?1)`,
		`DELETE FROM logs WHERE namespace=?1`,
		`DELETE FROM tasks WHERE namespace=?1`,
		`DELETE FROM waits WHERE namespace=?1`,
		`DELETE FROM runs WHERE namespace=?1`,
		`DELETE FROM workflows WHERE namespace=?1`,
		`DELETE FROM events WHERE namespace=?1`,
		`DELETE FROM secrets WHERE namespace=?1`,
		`DELETE FROM access_tokens WHERE namespace=?1`,
		`DELETE FROM bundle_owners WHERE namespace=?1`,
		`DELETE FROM schedules WHERE namespace=?1`,
		`DELETE FROM join_tokens WHERE pool IN (SELECT name FROM pools WHERE namespace=?1)`,
		`DELETE FROM workers WHERE namespace=?1`,
		`DELETE FROM pools WHERE namespace=?1`,
	} {
		if _, err := r.q.ExecContext(ctx, q, name); err != nil {
			return err
		}
	}
	return nil
}

func (r namespaceRepo) Stats(ctx context.Context, name string) (tenant.Stats, error) {
	var s tenant.Stats
	err := r.q.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(DISTINCT name) FROM workflows WHERE namespace=?1),
		(SELECT COUNT(*) FROM runs WHERE namespace=?1 AND finished_at IS NULL),
		(SELECT COUNT(*) FROM workers WHERE namespace=?1),
		(SELECT COUNT(*) FROM access_tokens WHERE namespace=?1)`, name).
		Scan(&s.Workflows, &s.ActiveRuns, &s.Workers, &s.Tokens)
	return s, err
}

// --------------------------------------------------------- access tokens ----

type accessTokenRepo struct{ q querier }

const accessTokenCols = `id, secret_hash, namespace, name, expires_at, created_at`

func scanAccessToken(sc interface{ Scan(...any) error }) (tenant.AccessToken, error) {
	var t tenant.AccessToken
	var exp sql.NullInt64
	var created int64
	if err := sc.Scan(&t.ID, &t.SecretHash, &t.Namespace, &t.Name, &exp, &created); err != nil {
		return t, notFound(err)
	}
	t.ExpiresAt, t.CreatedAt = tPtr(exp), tFrom(created)
	return t, nil
}

func (r accessTokenRepo) Create(ctx context.Context, t tenant.AccessToken) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO access_tokens (`+accessTokenCols+`) VALUES (?,?,?,?,?,?)`,
		t.ID, t.SecretHash, t.Namespace, t.Name, nsPtr(t.ExpiresAt), ns(t.CreatedAt))
	return err
}

func (r accessTokenRepo) Get(ctx context.Context, id string) (tenant.AccessToken, error) {
	return scanAccessToken(r.q.QueryRowContext(ctx, `SELECT `+accessTokenCols+` FROM access_tokens WHERE id=?`, id))
}

func (r accessTokenRepo) List(ctx context.Context, nsp string) ([]tenant.AccessToken, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+accessTokenCols+` FROM access_tokens WHERE namespace=? ORDER BY created_at`, nsp)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant.AccessToken
	for rows.Next() {
		t, err := scanAccessToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r accessTokenRepo) Delete(ctx context.Context, nsp, id string) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM access_tokens WHERE namespace=? AND id=?`, nsp, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ports.ErrNotFound
	}
	return nil
}

// --------------------------------------------------------------- bundles ----

type bundleRepo struct{ q querier }

func (r bundleRepo) AddOwner(ctx context.Context, nsp, digest string, now time.Time) error {
	_, err := r.q.ExecContext(ctx, `INSERT OR IGNORE INTO bundle_owners (namespace, digest, created_at) VALUES (?,?,?)`, nsp, digest, ns(now))
	return err
}

func (r bundleRepo) UploadedAt(ctx context.Context, nsp, digest string) (*time.Time, error) {
	var t sql.NullInt64
	err := r.q.QueryRowContext(ctx, `SELECT MIN(created_at) FROM bundle_owners WHERE digest=? AND namespace IN (?, '')`, digest, nsp).Scan(&t)
	return tPtr(t), err
}

func (r bundleRepo) Usable(ctx context.Context, nsp, digest string) (bool, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM bundle_owners WHERE digest=? AND namespace IN (?, '')`, digest, nsp).Scan(&n)
	return n > 0, err
}
