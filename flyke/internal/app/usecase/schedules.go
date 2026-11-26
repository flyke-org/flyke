package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/schedule"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

// Schedules start runs of a workflow at recurring times.
//
// Cost model: every schedule stores its next tick in an indexed column. The
// controller reads the schedules that are due (one index range scan), starts
// their runs, and sleeps until the earliest remaining tick. Nothing is parsed
// or loaded for schedules that are not due, so a thousand daily schedules
// cost the same as none between two ticks.

// scheduleBatch bounds the runs started in one controller pass.
const scheduleBatch = 100

// ----------------------------------------------------------------- cron ----

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

type cronSpec struct {
	sched cron.Schedule
	loc   *time.Location
}

const cronHelp = `use 5 fields "minute hour day-of-month month day-of-week" (e.g. "0 3 * * *"), ` +
	`a shortcut (@hourly, @daily, @weekly, @monthly) or an interval ("@every 15m")`

// parseCron validates a cron expression and its time zone ("" = UTC).
func parseCron(expr, tz string) (cronSpec, error) {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "TZ=") || strings.HasPrefix(expr, "CRON_TZ=") {
		return cronSpec{}, invalid("cron %q: set the time zone with tz, not inside the expression", expr)
	}
	loc := time.UTC
	if tz != "" {
		var err error
		if loc, err = time.LoadLocation(tz); err != nil {
			return cronSpec{}, invalid("unknown time zone %q: use an IANA name such as Europe/Paris or America/New_York", tz)
		}
	}
	sched, err := cronParser.Parse("CRON_TZ=" + loc.String() + " " + expr)
	if err != nil {
		return cronSpec{}, invalid("cron %q: %v; %s", expr, err, cronHelp)
	}
	return cronSpec{sched: sched, loc: loc}, nil
}

// next returns the first tick after t (zero if the expression never fires).
func (c cronSpec) next(after time.Time) time.Time { return c.sched.Next(after).UTC() }

func (c cronSpec) upcoming(after time.Time, n int) []time.Time {
	out := make([]time.Time, 0, n)
	for len(out) < n {
		if after = c.next(after); after.IsZero() {
			break
		}
		out = append(out, after)
	}
	return out
}

// check rejects expressions that never fire or fire too often.
func (c cronSpec) check(expr string, now time.Time) error {
	ticks := c.upcoming(now, 3)
	if len(ticks) == 0 {
		return invalid("cron %q never fires", expr)
	}
	for i := 1; i < len(ticks); i++ {
		if ticks[i].Sub(ticks[i-1]) < schedule.MinInterval {
			return invalid("cron %q fires more often than every %s, the shortest interval allowed", expr, schedule.MinInterval)
		}
	}
	return nil
}

// scheduleVars are the values input templates can use.
func scheduleVars(sc *schedule.Schedule, tick time.Time, loc *time.Location) map[string]any {
	local := tick.In(loc)
	return map[string]any{"schedule": map[string]any{
		"name": sc.Name,
		"time": local.Format(time.RFC3339),
		"date": local.Format(time.DateOnly),
	}}
}

// ---------------------------------------------------------------- reads ----

// ScheduleInfo is a schedule with what clients show next to it.
type ScheduleInfo struct {
	*schedule.Schedule
	// Description is the cron expression in plain English.
	Description string
	// Upcoming lists the next ticks (GetSchedule only; empty while paused).
	Upcoming []time.Time
	// LastRunPhase is the phase of LastRunID, if that run still exists.
	LastRunPhase run.Phase
}

// SchedulePreview explains a cron expression before it is saved.
type SchedulePreview struct {
	Description string
	Upcoming    []time.Time
}

func (s *Service) scheduleInfo(ctx context.Context, r ports.Repos, sc *schedule.Schedule, upcoming int) ScheduleInfo {
	info := ScheduleInfo{Schedule: sc, Description: schedule.Describe(sc.Cron, sc.TZ)}
	if sc.LastRunID != "" {
		if last, err := r.Runs().Get(ctx, sc.Namespace, sc.LastRunID); err == nil {
			info.LastRunPhase = last.Phase
		}
	}
	if upcoming > 0 && sc.NextRunAt != nil {
		// Start from the stored tick: an interval counts from the last
		// tick, not from now.
		info.Upcoming = []time.Time{*sc.NextRunAt}
		if spec, err := parseCron(sc.Cron, sc.TZ); err == nil {
			info.Upcoming = append(info.Upcoming, spec.upcoming(*sc.NextRunAt, upcoming-1)...)
		}
	}
	return info
}

func (s *Service) GetSchedule(ctx context.Context, ns, name string) (out ScheduleInfo, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		sc, err := r.Schedules().Get(ctx, ns, name)
		if err != nil {
			return fmt.Errorf("schedule %s: %w", name, err)
		}
		out = s.scheduleInfo(ctx, r, sc, 5)
		return nil
	})
	return
}

// ListSchedules lists a namespace's schedules ("" workflow = all of them).
func (s *Service) ListSchedules(ctx context.Context, ns, workflowName string) (out []ScheduleInfo, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		all, err := r.Schedules().List(ctx, ns, workflowName)
		if err != nil {
			return err
		}
		for _, sc := range all {
			out = append(out, s.scheduleInfo(ctx, r, sc, 0))
		}
		return nil
	})
	return
}

// PreviewSchedule validates a cron expression and shows when it would fire.
func (s *Service) PreviewSchedule(expr, tz string, n int) (SchedulePreview, error) {
	spec, err := parseCron(expr, tz)
	if err != nil {
		return SchedulePreview{}, err
	}
	now := s.clock.Now().UTC()
	if err := spec.check(expr, now); err != nil {
		return SchedulePreview{}, err
	}
	if n <= 0 || n > 20 {
		n = 5
	}
	return SchedulePreview{Description: schedule.Describe(expr, tz), Upcoming: spec.upcoming(now, n)}, nil
}

// --------------------------------------------------------------- writes ----

// checkSchedule validates sc against the cron parser and the workflow it
// starts, so a mistake is reported when the schedule is saved rather than
// when it first fires.
func (t *txn) checkSchedule(sc *schedule.Schedule, wf workflow.Workflow) (cronSpec, error) {
	if err := sc.Validate(); err != nil {
		return cronSpec{}, invalid("%v", err)
	}
	spec, err := parseCron(sc.Cron, sc.TZ)
	if err != nil {
		return spec, err
	}
	if err := spec.check(sc.Cron, t.now); err != nil {
		return spec, err
	}
	raw, err := renderInputs(sc.Inputs, scheduleVars(sc, t.now, spec.loc))
	if err != nil {
		return spec, invalid("schedule %s inputs: %v (templates: {schedule.time}, {schedule.date}, {schedule.name})", sc.Name, err)
	}
	if _, _, err := t.s.prepareInputs(wf, raw); err != nil {
		return spec, fmt.Errorf("schedule %s: %w", sc.Name, err)
	}
	return spec, nil
}

// plan sets the next tick from the schedule's state.
func plan(sc *schedule.Schedule, spec cronSpec, now time.Time) {
	if sc.Paused {
		sc.NextRunAt = nil
		return
	}
	next := spec.next(now)
	sc.NextRunAt = &next
}

// PutSchedule creates or updates a schedule. It reports whether it was created.
func (s *Service) PutSchedule(ctx context.Context, in schedule.Schedule) (ScheduleInfo, bool, error) {
	var out ScheduleInfo
	var created bool
	in.Declared = false
	err := s.tx(ctx, func(t *txn) error {
		wf, err := t.Workflows().GetLatest(ctx, in.Namespace, in.Workflow)
		if errors.Is(err, ports.ErrNotFound) && in.Workflow != "" {
			return invalid("workflow %s is not deployed in namespace %s: deploy it first (flyke deploy)", in.Workflow, in.Namespace)
		}
		if err != nil && in.Workflow != "" {
			return err
		}
		spec, err := t.checkSchedule(&in, wf)
		if err != nil {
			return err
		}
		cur, err := t.Schedules().Get(ctx, in.Namespace, in.Name)
		switch {
		case err == nil && cur.Declared:
			return declaredInCode(cur)
		case err == nil:
			in.CreatedAt, in.LastRunAt, in.LastRunID = cur.CreatedAt, cur.LastRunAt, cur.LastRunID
		case errors.Is(err, ports.ErrNotFound):
			created, in.CreatedAt = true, t.now
		default:
			return err
		}
		in.UpdatedAt = t.now
		plan(&in, spec, t.now)
		if err := t.Schedules().Put(ctx, &in); err != nil {
			return err
		}
		t.notify(ports.TopicSchedules)
		out = s.scheduleInfo(ctx, t, &in, 5)
		return nil
	})
	return out, created, err
}

func declaredInCode(sc *schedule.Schedule) error {
	return precondition("schedule %s is declared in the code of workflow %s: change it there and deploy again (it can still be paused, resumed and triggered)",
		sc.Name, sc.Workflow)
}

func (s *Service) DeleteSchedule(ctx context.Context, ns, name string) error {
	return s.tx(ctx, func(t *txn) error {
		cur, err := t.Schedules().Get(ctx, ns, name)
		if err != nil {
			return fmt.Errorf("schedule %s: %w", name, err)
		}
		if cur.Declared {
			return declaredInCode(cur)
		}
		t.notify(ports.TopicSchedules)
		return t.Schedules().Delete(ctx, ns, name)
	})
}

// SetSchedulePaused pauses or resumes a schedule. Ticks that pass while a
// schedule is paused are never caught up: resuming waits for the next one.
func (s *Service) SetSchedulePaused(ctx context.Context, ns, name string, paused bool) (out ScheduleInfo, err error) {
	err = s.tx(ctx, func(t *txn) error {
		sc, err := t.Schedules().Get(ctx, ns, name)
		if err != nil {
			return fmt.Errorf("schedule %s: %w", name, err)
		}
		if sc.Paused != paused {
			spec, err := parseCron(sc.Cron, sc.TZ)
			if err != nil {
				return err
			}
			sc.Paused, sc.UpdatedAt = paused, t.now
			plan(sc, spec, t.now)
			if err := t.Schedules().Put(ctx, sc); err != nil {
				return err
			}
			t.notify(ports.TopicSchedules)
		}
		out = s.scheduleInfo(ctx, t, sc, 5)
		return nil
	})
	return
}

// TriggerSchedule starts a run of a schedule now, with its inputs, without
// changing when it fires next. It works on paused schedules too.
func (s *Service) TriggerSchedule(ctx context.Context, ns, name string) (*run.Run, error) {
	var out *run.Run
	err := s.tx(ctx, func(t *txn) error {
		sc, err := t.Schedules().Get(ctx, ns, name)
		if err != nil {
			return fmt.Errorf("schedule %s: %w", name, err)
		}
		spec, err := parseCron(sc.Cron, sc.TZ)
		if err != nil {
			return err
		}
		if out, err = t.startScheduledRun(sc, spec, t.now, ""); err != nil {
			return err
		}
		sc.LastRunAt, sc.LastRunID, sc.LastError = ptrTime(t.now), out.ID, ""
		return t.Schedules().Put(ctx, sc)
	})
	return out, err
}

func (t *txn) startScheduledRun(sc *schedule.Schedule, spec cronSpec, tick time.Time, idempotencyKey string) (*run.Run, error) {
	wf, err := t.Workflows().GetLatest(t.ctx, sc.Namespace, sc.Workflow)
	if err != nil {
		return nil, fmt.Errorf("workflow %s: %w", sc.Workflow, err)
	}
	inputs, err := renderInputs(sc.Inputs, scheduleVars(sc, tick, spec.loc))
	if err != nil {
		return nil, invalid("inputs: %v", err)
	}
	return t.startRun(wf, StartRunInput{
		Inputs: inputs, IdempotencyKey: idempotencyKey, TriggeredBy: "schedule:" + sc.Name,
		Labels: map[string]string{"flyke.io/schedule": sc.Name},
	})
}

// ------------------------------------------------------------ controller ----

// ReconcileSchedules starts a run for every schedule whose tick is due and
// returns how long until the next tick (0 = no active schedule). Ticks missed
// while the control plane was down collapse into a single run, or into none
// under the skip policy.
func (s *Service) ReconcileSchedules(ctx context.Context) (time.Duration, error) {
	var wait time.Duration
	err := s.tx(ctx, func(t *txn) error {
		due, err := t.Schedules().ListDue(ctx, t.now, scheduleBatch)
		if err != nil {
			return err
		}
		for _, sc := range due {
			t.fireSchedule(sc)
			if err := t.Schedules().Put(ctx, sc); err != nil {
				return err
			}
		}
		if len(due) == scheduleBatch {
			wait = time.Millisecond // more are due: come straight back
			return nil
		}
		next, err := t.Schedules().NextDue(ctx)
		if err != nil || next.IsZero() {
			return err
		}
		wait = max(next.Sub(t.now), time.Millisecond)
		return nil
	})
	return wait, err
}

// fireSchedule handles one due tick and moves the schedule to its next one.
func (t *txn) fireSchedule(sc *schedule.Schedule) {
	tick := *sc.NextRunAt
	spec, err := parseCron(sc.Cron, sc.TZ)
	if err != nil {
		// Cannot happen for a schedule saved through checkSchedule; park
		// it rather than fail every pass.
		sc.NextRunAt, sc.LastError = nil, err.Error()
		return
	}
	plan(sc, spec, t.now)
	if late := t.now.Sub(tick); sc.Missed == schedule.Skip && late > schedule.MissedGrace {
		t.s.log.Info("schedule skipped a missed tick", "namespace", sc.Namespace, "schedule", sc.Name, "tick", tick, "late", late)
		return
	}
	r, err := t.startScheduledRun(sc, spec, tick, fmt.Sprintf("schedule:%s:%d", sc.Name, tick.Unix()))
	if err != nil {
		sc.LastError = err.Error()
		t.s.log.Warn("schedule could not start a run", "namespace", sc.Namespace, "schedule", sc.Name, "workflow", sc.Workflow, "err", err)
		return
	}
	sc.LastRunAt, sc.LastRunID, sc.LastError = &tick, r.ID, ""
}

// ------------------------------------------------- declared in workflows ----

// declaredSchedules builds the schedules a manifest declares with
// on.schedule / on.every.
func declaredSchedules(wf workflow.Workflow) ([]*schedule.Schedule, error) {
	var out []*schedule.Schedule
	seen := map[string]bool{}
	unnamed := 0
	for _, tr := range wf.Manifest.Triggers {
		if tr.Type != "schedule" {
			continue
		}
		name := tr.Name
		if name == "" {
			name = schedule.DefaultName(wf.Name, unnamed)
			unnamed++
		}
		if seen[name] {
			return nil, invalid("workflow %s declares two schedules named %q: give each a name with on.schedule(..., name=…)", wf.Name, name)
		}
		seen[name] = true
		out = append(out, &schedule.Schedule{
			Namespace: wf.Namespace, Name: name, Workflow: wf.Name, Cron: tr.Cron, TZ: tr.TZ,
			Inputs: tr.Inputs, Missed: schedule.MissedPolicy(tr.Missed), Declared: true,
		})
	}
	return out, nil
}

// syncDeclaredSchedules makes the stored schedules of wf match its manifest:
// new ones are created, changed ones updated (keeping their paused state and
// last run), and those no longer declared are removed. Schedules created
// through the API are never touched.
func (t *txn) syncDeclaredSchedules(wf workflow.Workflow) error {
	want, err := declaredSchedules(wf)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, sc := range want {
		spec, err := t.checkSchedule(sc, wf)
		if err != nil {
			return err
		}
		keep[sc.Name] = true
		cur, err := t.Schedules().Get(t.ctx, sc.Namespace, sc.Name)
		switch {
		case errors.Is(err, ports.ErrNotFound):
			sc.CreatedAt = t.now
		case err != nil:
			return err
		case !cur.Declared || cur.Workflow != wf.Name:
			return invalid("workflow %s declares a schedule named %q, but a schedule with that name already exists for workflow %s: "+
				"choose another name with on.schedule(..., name=…) or delete the existing one", wf.Name, sc.Name, cur.Workflow)
		default:
			sc.CreatedAt, sc.Paused = cur.CreatedAt, cur.Paused
			sc.LastRunAt, sc.LastRunID, sc.LastError = cur.LastRunAt, cur.LastRunID, cur.LastError
			if cur.Cron == sc.Cron && cur.TZ == sc.TZ {
				sc.NextRunAt = cur.NextRunAt // same timetable: keep a tick that is already due
			}
		}
		sc.UpdatedAt = t.now
		if sc.Paused || sc.NextRunAt == nil {
			plan(sc, spec, t.now)
		}
		if err := t.Schedules().Put(t.ctx, sc); err != nil {
			return err
		}
	}
	existing, err := t.Schedules().List(t.ctx, wf.Namespace, wf.Name)
	if err != nil {
		return err
	}
	for _, cur := range existing {
		if cur.Declared && !keep[cur.Name] {
			if err := t.Schedules().Delete(t.ctx, cur.Namespace, cur.Name); err != nil {
				return err
			}
		}
	}
	t.notify(ports.TopicSchedules)
	return nil
}

// AdoptDeclaredSchedules creates the schedules that deployed workflows
// declare and that are not stored yet. It runs once at startup, for
// databases written before schedules were stored on their own.
func (s *Service) AdoptDeclaredSchedules(ctx context.Context) error {
	return s.tx(ctx, func(t *txn) error {
		wfs, err := t.Workflows().ListLatest(ctx, "")
		if err != nil {
			return err
		}
		for _, wf := range wfs {
			declares := false
			for _, tr := range wf.Manifest.Triggers {
				declares = declares || tr.Type == "schedule"
			}
			if !declares {
				continue
			}
			if err := t.syncDeclaredSchedules(wf); err != nil {
				s.log.Warn("schedules declared by a workflow were not adopted", "namespace", wf.Namespace, "workflow", wf.Name, "err", err)
			}
		}
		return nil
	})
}
