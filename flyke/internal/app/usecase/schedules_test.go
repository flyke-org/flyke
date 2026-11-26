package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/adapter/notify"
	"github.com/flyke-org/flyke/flyke/internal/adapter/store/sqlite"
	"github.com/flyke-org/flyke/flyke/internal/adapter/system"
	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/app/usecase"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/schedule"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) set(t time.Time) { c.mu.Lock(); c.now = t; c.mu.Unlock() }

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

const reportManifest = `{
  "name": "Report", "entrypoint": "r.py:Report", "runtime": "python/3.12",
  "inputs_schema": {"type":"object","properties":{"day":{"type":"string"},"kind":{"type":"string","default":"full"}},"additionalProperties":false},
  "tasks": {}%s
}`

type fixture struct {
	t     *testing.T
	svc   *usecase.Service
	clock *fakeClock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	clock := &fakeClock{now: at("2026-03-10T12:00:00Z")}
	svc := usecase.New(usecase.Deps{
		Store: store, Notifier: notify.NewMemory(), Clock: clock, IDs: system.IDs{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, usecase.Config{})
	f := &fixture{t: t, svc: svc, clock: clock}
	f.deploy("")
	return f
}

// deploy registers the Report workflow; triggers is a JSON array or "".
func (f *fixture) deploy(triggers string) error {
	extra := ""
	if triggers != "" {
		extra = `, "triggers": ` + triggers
	}
	_, err := f.svc.RegisterWorkflow(context.Background(), "default", "Report",
		json.RawMessage(strings.Replace(reportManifest, "%s", extra, 1)), workflow.Source{LocalRoot: "/code"})
	return err
}

func (f *fixture) put(sc schedule.Schedule) usecase.ScheduleInfo {
	f.t.Helper()
	if sc.Namespace == "" {
		sc.Namespace = "default"
	}
	if sc.Workflow == "" {
		sc.Workflow = "Report"
	}
	out, _, err := f.svc.PutSchedule(context.Background(), sc)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) reconcile() time.Duration {
	f.t.Helper()
	wait, err := f.svc.ReconcileSchedules(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return wait
}

func (f *fixture) runs() []*run.Run {
	f.t.Helper()
	out, err := f.svc.ListRuns(context.Background(), ports.RunFilter{Namespace: "default"})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) get(name string) usecase.ScheduleInfo {
	f.t.Helper()
	out, err := f.svc.GetSchedule(context.Background(), "default", name)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestScheduleFiresOnTimeInItsTimeZone(t *testing.T) {
	f := newFixture(t)
	sc := f.put(schedule.Schedule{Name: "nightly", Cron: "0 3 * * *", TZ: "Europe/Paris", Inputs: map[string]any{"day": "{schedule.date}"}})

	// 03:00 in Paris is 02:00 UTC in winter.
	tick := at("2026-03-11T02:00:00Z")
	if sc.NextRunAt == nil || !sc.NextRunAt.Equal(tick) {
		t.Fatalf("next run = %v, want %v", sc.NextRunAt, tick)
	}
	if sc.Description != "Every day at 03:00 (Europe/Paris)" || len(sc.Upcoming) != 5 {
		t.Fatalf("description %q, %d upcoming", sc.Description, len(sc.Upcoming))
	}

	// Nothing is due: the controller is told to sleep until the tick.
	if wait := f.reconcile(); wait != 14*time.Hour {
		t.Fatalf("wait = %v, want 14h", wait)
	}
	if n := len(f.runs()); n != 0 {
		t.Fatalf("%d runs before the tick", n)
	}

	f.clock.set(tick.Add(2 * time.Second))
	f.reconcile()
	f.reconcile() // a second pass must not start it again
	runs := f.runs()
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1", len(runs))
	}
	r := runs[0]
	if r.TriggeredBy != "schedule:nightly" || r.Labels["flyke.io/schedule"] != "nightly" {
		t.Fatalf("triggeredBy %q labels %v", r.TriggeredBy, r.Labels)
	}
	if string(r.Inputs) != `{"day":"2026-03-11","kind":"full"}` {
		t.Fatalf("inputs %s", r.Inputs)
	}
	got := f.get("nightly")
	if got.LastRunID != r.ID || !got.LastRunAt.Equal(tick) || !got.NextRunAt.Equal(tick.Add(24*time.Hour)) || got.LastRunPhase != run.Pending {
		t.Fatalf("after firing: %+v", got.Schedule)
	}
}

func TestMissedTicks(t *testing.T) {
	f := newFixture(t)
	f.put(schedule.Schedule{Name: "catch-up", Cron: "@hourly"})
	f.put(schedule.Schedule{Name: "drop", Cron: "@hourly", Missed: schedule.Skip})

	// The control plane was down for three days.
	f.clock.set(at("2026-03-13T12:30:00Z"))
	f.reconcile()
	runs := f.runs()
	if len(runs) != 1 || runs[0].TriggeredBy != "schedule:catch-up" {
		t.Fatalf("want exactly one catch-up run, got %d", len(runs))
	}
	for _, name := range []string{"catch-up", "drop"} {
		if next := f.get(name).NextRunAt; !next.Equal(at("2026-03-13T13:00:00Z")) {
			t.Fatalf("%s next run = %v", name, next)
		}
	}

	// On time again: both fire.
	f.clock.set(at("2026-03-13T13:00:01Z"))
	f.reconcile()
	if n := len(f.runs()); n != 3 {
		t.Fatalf("%d runs, want 3", n)
	}
}

func TestPauseResumeAndTrigger(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.put(schedule.Schedule{Name: "hourly", Cron: "@hourly"})

	sc, err := f.svc.SetSchedulePaused(ctx, "default", "hourly", true)
	if err != nil || !sc.Paused || sc.NextRunAt != nil || len(sc.Upcoming) != 0 {
		t.Fatalf("pause: %+v %v", sc.Schedule, err)
	}
	f.clock.set(at("2026-03-10T15:10:00Z"))
	if wait := f.reconcile(); wait != 0 {
		t.Fatalf("wait = %v with only a paused schedule", wait)
	}
	if n := len(f.runs()); n != 0 {
		t.Fatalf("a paused schedule started %d runs", n)
	}

	// A paused schedule can still be run by hand.
	r, err := f.svc.TriggerSchedule(ctx, "default", "hourly")
	if err != nil || r.TriggeredBy != "schedule:hourly" {
		t.Fatalf("trigger: %v", err)
	}

	// Resuming does not catch up on the ticks that passed.
	sc, err = f.svc.SetSchedulePaused(ctx, "default", "hourly", false)
	if err != nil || !sc.NextRunAt.Equal(at("2026-03-10T16:00:00Z")) {
		t.Fatalf("resume: next %v err %v", sc.NextRunAt, err)
	}
	f.reconcile()
	if n := len(f.runs()); n != 1 {
		t.Fatalf("%d runs after resume, want only the manual one", n)
	}
}

func TestScheduleValidation(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		name string
		sc   schedule.Schedule
		want string
	}{
		{"bad cron", schedule.Schedule{Name: "x", Workflow: "Report", Cron: "0 3 * *"}, "5 fields"},
		{"bad zone", schedule.Schedule{Name: "x", Workflow: "Report", Cron: "@daily", TZ: "Paris"}, "IANA"},
		{"too often", schedule.Schedule{Name: "x", Workflow: "Report", Cron: "@every 2s"}, "more often"},
		{"never", schedule.Schedule{Name: "x", Workflow: "Report", Cron: "0 0 31 2 *"}, "never fires"},
		{"bad name", schedule.Schedule{Name: "Nightly Job", Workflow: "Report", Cron: "@daily"}, "lowercase"},
		{"no workflow", schedule.Schedule{Name: "x", Workflow: "Nope", Cron: "@daily"}, "not deployed"},
		{"bad input", schedule.Schedule{Name: "x", Workflow: "Report", Cron: "@daily", Inputs: map[string]any{"nope": 1}}, "schema"},
		{"bad template", schedule.Schedule{Name: "x", Workflow: "Report", Cron: "@daily", Inputs: map[string]any{"day": "{schedule.day}"}}, "{schedule.date}"},
		{"bad policy", schedule.Schedule{Name: "x", Workflow: "Report", Cron: "@daily", Missed: "backfill"}, "run_once or skip"},
	} {
		c.sc.Namespace = "default"
		_, _, err := f.svc.PutSchedule(context.Background(), c.sc)
		if !errors.Is(err, usecase.ErrInvalidArgument) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an invalid-argument error mentioning %q", c.name, err, c.want)
		}
	}
}

func TestSchedulesDeclaredInCodeFollowDeploys(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	names := func() string {
		scs, err := f.svc.ListSchedules(ctx, "default", "Report")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, sc := range scs {
			out = append(out, sc.Name)
		}
		return strings.Join(out, ",")
	}

	if err := f.deploy(`[{"type":"schedule","cron":"0 3 * * *"},{"type":"schedule","cron":"@every 30m","name":"refresh","missed":"skip"}]`); err != nil {
		t.Fatal(err)
	}
	if got := names(); got != "refresh,report" {
		t.Fatalf("schedules %q", got)
	}
	if sc := f.get("refresh"); !sc.Declared || sc.Missed != schedule.Skip {
		t.Fatalf("refresh: %+v", sc.Schedule)
	}

	// They are changed in code, not through the API...
	_, _, err := f.svc.PutSchedule(ctx, schedule.Schedule{Namespace: "default", Name: "report", Workflow: "Report", Cron: "@daily"})
	if !errors.Is(err, usecase.ErrFailedPrecondition) {
		t.Fatalf("editing a declared schedule: %v", err)
	}
	if err := f.svc.DeleteSchedule(ctx, "default", "report"); !errors.Is(err, usecase.ErrFailedPrecondition) {
		t.Fatalf("deleting a declared schedule: %v", err)
	}
	// ...but can be paused, and stay paused across deploys.
	if _, err := f.svc.SetSchedulePaused(ctx, "default", "report", true); err != nil {
		t.Fatal(err)
	}
	f.put(schedule.Schedule{Name: "by-hand", Cron: "@weekly"})

	if err := f.deploy(`[{"type":"schedule","cron":"0 4 * * *"}]`); err != nil {
		t.Fatal(err)
	}
	if got := names(); got != "by-hand,report" {
		t.Fatalf("schedules after redeploy %q", got)
	}
	if sc := f.get("report"); !sc.Paused || sc.Cron != "0 4 * * *" || sc.NextRunAt != nil {
		t.Fatalf("report after redeploy: %+v", sc.Schedule)
	}

	// A deploy is rejected rather than taking over someone's schedule.
	err = f.deploy(`[{"type":"schedule","cron":"@daily","name":"by-hand"}]`)
	if !errors.Is(err, usecase.ErrInvalidArgument) || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("name clash: %v", err)
	}
	if err := f.deploy(`[{"type":"schedule","cron":"61 * * * *"}]`); !errors.Is(err, usecase.ErrInvalidArgument) {
		t.Fatalf("invalid cron in code: %v", err)
	}
	if got := names(); got != "by-hand,report" {
		t.Fatalf("a rejected deploy changed schedules: %q", got)
	}
}
