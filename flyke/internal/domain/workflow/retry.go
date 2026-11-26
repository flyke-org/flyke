package workflow

import (
	"errors"
	"fmt"
	"math"
	"time"
)

type Backoff string

const (
	BackoffFixed       Backoff = "fixed"
	BackoffLinear      Backoff = "linear"
	BackoffExponential Backoff = "exp"
)

// RetryPolicy decides whether and when a failed attempt is retried.
type RetryPolicy struct {
	// Attempts is the total number of attempts (1 = no retry).
	Attempts   int     `json:"attempts"`
	Backoff    Backoff `json:"backoff"`
	DelayMs    int64   `json:"delay_ms"`
	MaxDelayMs int64   `json:"max_delay_ms,omitempty"`
	// On restricts retries to these fully qualified error types (and subtypes).
	// Empty means any error is retryable.
	On []string `json:"on,omitempty"`
}

// DefaultRetry applies when a task declares no retry policy.
var DefaultRetry = RetryPolicy{Attempts: 3, Backoff: BackoffExponential, DelayMs: 1000, MaxDelayMs: 30_000}

func (p *RetryPolicy) normalize() error {
	if p.Attempts == 0 && p.Backoff == "" && p.DelayMs == 0 {
		*p = DefaultRetry
		return nil
	}
	if p.Attempts < 1 {
		return errors.New("retry.attempts must be >= 1")
	}
	switch p.Backoff {
	case "":
		p.Backoff = BackoffExponential
	case BackoffFixed, BackoffLinear, BackoffExponential:
	default:
		return fmt.Errorf("unknown backoff %q", p.Backoff)
	}
	if p.DelayMs < 0 || p.MaxDelayMs < 0 {
		return errors.New("retry delays must be >= 0")
	}
	return nil
}

// Failure is a language-neutral description of an error raised by user code.
type Failure struct {
	Type         string   `json:"type"`
	MRO          []string `json:"mro,omitempty"`
	Message      string   `json:"message"`
	Stack        string   `json:"stack,omitempty"`
	NonRetryable bool     `json:"non_retryable,omitempty"`
}

func (f Failure) String() string {
	if f.Message == "" {
		return f.Type
	}
	return fmt.Sprintf("%s: %s", shortType(f.Type), f.Message)
}

func shortType(t string) string {
	for i := len(t) - 1; i >= 0; i-- {
		if t[i] == '.' {
			return t[i+1:]
		}
	}
	return t
}

// Matches reports whether the failure is (a subtype of) one of the given types.
func (f Failure) Matches(types []string) bool {
	hierarchy := f.MRO
	if len(hierarchy) == 0 {
		hierarchy = []string{f.Type}
	}
	for _, want := range types {
		for _, have := range hierarchy {
			if have == want {
				return true
			}
		}
	}
	return false
}

// Decision is the outcome of applying a retry policy to a failure.
type Decision struct {
	Retry bool
	Delay time.Duration
	// Reason explains a negative decision.
	Reason string
}

// Decide applies the policy to the failure of attempt number `attempt` (1-based).
func (p RetryPolicy) Decide(f Failure, attempt int) Decision {
	switch {
	case f.NonRetryable:
		return Decision{Reason: "non-retryable error"}
	case len(p.On) > 0 && !f.Matches(p.On):
		return Decision{Reason: "error type not in retry.on"}
	case attempt >= p.Attempts:
		return Decision{Reason: fmt.Sprintf("%d/%d attempts exhausted", attempt, p.Attempts)}
	}
	return Decision{Retry: true, Delay: p.delay(attempt)}
}

func (p RetryPolicy) delay(attempt int) time.Duration {
	base := float64(p.DelayMs)
	var ms float64
	switch p.Backoff {
	case BackoffFixed:
		ms = base
	case BackoffLinear:
		ms = base * float64(attempt)
	default:
		ms = base * math.Pow(2, float64(attempt-1))
	}
	if p.MaxDelayMs > 0 && ms > float64(p.MaxDelayMs) {
		ms = float64(p.MaxDelayMs)
	}
	return time.Duration(ms) * time.Millisecond
}

// DecideLost applies the policy to an attempt whose worker was lost. The
// error-type filter does not apply: infrastructure loss is always retryable
// for tasks that allow retries.
func (p RetryPolicy) DecideLost(attempt int) Decision {
	if attempt >= p.Attempts {
		return Decision{Reason: fmt.Sprintf("%d/%d attempts exhausted", attempt, p.Attempts)}
	}
	return Decision{Retry: true, Delay: p.delay(attempt)}
}
