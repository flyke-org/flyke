package run

import (
	"errors"
	"testing"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

func TestRunLifecycleWithCompensation(t *testing.T) {
	now := time.Now()
	r := &Run{Phase: Pending}
	if err := r.Admit(now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if r.Phase != Running || !r.NeedsStep || r.DeadlineAt == nil {
		t.Fatalf("admit: %+v", r)
	}
	r.NeedsStep = false
	r.Settle(false)
	if r.Phase != Waiting {
		t.Fatalf("expected Waiting, got %s", r.Phase)
	}
	r.Wake()
	if r.Phase != Running || !r.NeedsStep {
		t.Fatal("wake should resume Running")
	}
	f := &workflow.Failure{Type: "builtins.RuntimeError", Message: "boom"}
	if err := r.Finish(now, Failed, "TaskFailed", "boom", f, true); err != nil {
		t.Fatal(err)
	}
	if r.Phase != Compensating || r.TerminalCause != Failed {
		t.Fatalf("expected Compensating, got %+v", r)
	}
	if err := r.Succeed(now, nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("succeed while compensating must be rejected")
	}
	if err := r.CompleteCompensation(now, true); err != nil {
		t.Fatal(err)
	}
	if r.Phase != Failed || r.Compensation != CompensationCompleted || r.FinishedAt == nil {
		t.Fatalf("after compensation: %+v", r)
	}
	if err := r.Pause("x", "y"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("terminal run must not pause")
	}
}

func TestQueueAndSkip(t *testing.T) {
	r := &Run{Phase: Pending}
	if err := r.Queue("slot taken"); err != nil || r.Phase != Queued {
		t.Fatal("queue failed")
	}
	if err := r.Skip(time.Now(), "dup"); err != nil || r.Phase != Skipped || !r.Phase.Terminal() {
		t.Fatal("skip failed")
	}
}
