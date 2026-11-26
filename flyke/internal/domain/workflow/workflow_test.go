package workflow

import (
	"testing"
	"time"
)

func TestRetryDecide(t *testing.T) {
	p := RetryPolicy{Attempts: 5, Backoff: BackoffExponential, DelayMs: 200, MaxDelayMs: 2000, On: []string{"builtins.ConnectionError"}}
	connErr := Failure{Type: "builtins.ConnectionRefusedError", MRO: []string{"builtins.ConnectionRefusedError", "builtins.ConnectionError", "builtins.OSError"}}
	cases := []struct {
		name    string
		f       Failure
		attempt int
		retry   bool
		delay   time.Duration
	}{
		{"first failure", connErr, 1, true, 200 * time.Millisecond},
		{"exp growth", connErr, 3, true, 800 * time.Millisecond},
		{"capped", connErr, 4, true, 1600 * time.Millisecond},
		{"exhausted", connErr, 5, false, 0},
		{"type not listed", Failure{Type: "builtins.ValueError"}, 1, false, 0},
		{"non retryable", Failure{Type: "builtins.ConnectionError", NonRetryable: true}, 1, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := p.Decide(c.f, c.attempt)
			if d.Retry != c.retry || d.Delay != c.delay {
				t.Fatalf("got %+v, want retry=%v delay=%v", d, c.retry, c.delay)
			}
		})
	}
	big := RetryPolicy{Attempts: 10, Backoff: BackoffExponential, DelayMs: 200, MaxDelayMs: 2000}
	if d := big.Decide(Failure{Type: "x"}, 8); d.Delay != 2*time.Second {
		t.Fatalf("expected cap at 2s, got %v", d.Delay)
	}
}

func TestRender(t *testing.T) {
	vars := map[string]any{"service": "api", "env": "prod", "event": map[string]any{"repo": "billing"}}
	got, err := Render("{service}:{env}", vars)
	if err != nil || got != "api:prod" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := Render("{nope}", vars); err == nil {
		t.Fatal("expected error for unknown field")
	}
	v, err := RenderValue("{event.repo}", vars)
	if err != nil || v != "billing" {
		t.Fatalf("got %v, %v", v, err)
	}
}

func TestMatchWhere(t *testing.T) {
	payload := map[string]any{"branch": "main", "service": "api"}
	if !MatchWhere(map[string]any{"branch": "main"}, payload) {
		t.Fatal("expected match")
	}
	if MatchWhere(map[string]any{"branch": "dev"}, payload) {
		t.Fatal("expected no match")
	}
	if !MatchWhere(map[string]any{"branch": []any{"dev", "main"}}, payload) {
		t.Fatal("expected any-of match")
	}
}

func TestManifestValidateDefaults(t *testing.T) {
	m := Manifest{Name: "W", Entrypoint: "w.py:W", Tasks: map[string]TaskSpec{
		"a": {},
		"b": {Guarantee: AtMostOnce},
	}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if m.Tasks["a"].Retry.Attempts != DefaultRetry.Attempts || m.Tasks["a"].OnUnknown != OnUnknownRetry {
		t.Fatalf("defaults not applied: %+v", m.Tasks["a"])
	}
	if m.Tasks["b"].OnUnknown != OnUnknownFail {
		t.Fatalf("at_most_once must default to on_unknown=fail, got %s", m.Tasks["b"].OnUnknown)
	}
	bad := Manifest{Name: "W", Entrypoint: "w.py:W", Tasks: map[string]TaskSpec{"x": {Guarantee: AtMostOnce, OnUnknown: OnUnknownRetry}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("at_most_once + on_unknown=retry must be rejected")
	}
}
