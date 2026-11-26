// Package schedule models schedules: a named rule that starts runs of one
// workflow at recurring times.
//
// A schedule is either created through the API (CLI, dashboard) or declared
// in workflow code with on.schedule / on.every. Declared schedules follow the
// code: a deploy creates, updates and removes them, and they can only be
// paused, resumed and triggered from outside.
package schedule

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MissedPolicy says what to do with a tick that could not fire on time
// (the control plane was down or busy).
type MissedPolicy string

const (
	// RunOnce starts one run as soon as possible, however many ticks were
	// missed. This is the default.
	RunOnce MissedPolicy = "run_once"
	// Skip drops ticks that are more than MissedGrace late and waits for the
	// next one.
	Skip MissedPolicy = "skip"
)

const (
	// MissedGrace is how late a tick may fire under the Skip policy.
	MissedGrace = time.Minute
	// MinInterval is the shortest allowed time between two ticks.
	MinInterval = 10 * time.Second
)

var (
	ErrInvalid = errors.New("invalid schedule")
	nameRe     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
)

type Schedule struct {
	Namespace string
	Name      string
	Workflow  string
	// Cron is a 5-field cron expression or a descriptor (@hourly, @every 15m).
	Cron string
	// TZ is the IANA time zone the expression is read in ("" = UTC).
	TZ string
	// Inputs are the workflow inputs of each run; strings may use the
	// "{schedule.time}", "{schedule.date}" and "{schedule.name}" templates.
	Inputs map[string]any
	Missed MissedPolicy
	Paused bool
	// Declared marks a schedule that comes from workflow code.
	Declared bool

	// NextRunAt is the next tick (nil while paused).
	NextRunAt *time.Time
	// LastRunAt is the tick of the last run started; LastRunID is that run.
	LastRunAt *time.Time
	LastRunID string
	// LastError is why the last tick could not start a run ("" if it did).
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate checks the fields that need no cron parser and fills defaults.
func (s *Schedule) Validate() error {
	if !nameRe.MatchString(s.Name) {
		return fmt.Errorf("%w: name %q must be lowercase letters, digits and '-' (max 63)", ErrInvalid, s.Name)
	}
	if s.Workflow == "" {
		return fmt.Errorf("%w: a schedule needs a workflow", ErrInvalid)
	}
	s.Cron = strings.TrimSpace(s.Cron)
	if s.Cron == "" {
		return fmt.Errorf("%w: a schedule needs a cron expression (e.g. \"0 3 * * *\") or an interval (\"@every 15m\")", ErrInvalid)
	}
	switch s.Missed {
	case "":
		s.Missed = RunOnce
	case RunOnce, Skip:
	default:
		return fmt.Errorf("%w: unknown missed policy %q (want run_once or skip)", ErrInvalid, s.Missed)
	}
	return nil
}

// Zone is the time zone name shown to users.
func (s *Schedule) Zone() string {
	if s.TZ == "" {
		return "UTC"
	}
	return s.TZ
}

var camel = regexp.MustCompile(`([a-z0-9])([A-Z])`)
var notNameChar = regexp.MustCompile(`[^a-z0-9]+`)

// DefaultName names the n-th (0-based) unnamed schedule declared by a
// workflow: "NightlyReport" gives "nightly-report", then "nightly-report-2".
func DefaultName(workflow string, n int) string {
	base := strings.ToLower(camel.ReplaceAllString(workflow, "$1-$2"))
	base = strings.Trim(notNameChar.ReplaceAllString(base, "-"), "-")
	if base == "" {
		base = "schedule"
	}
	if len(base) > 56 {
		base = strings.TrimRight(base[:56], "-")
	}
	if n == 0 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, n+1)
}
