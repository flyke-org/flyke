package worker

import (
	"errors"
	"testing"
	"time"
)

func TestJoinTokenLifecycle(t *testing.T) {
	now := time.Now()
	tok, raw, err := NewJoinToken("gpu", now, time.Hour, 1, map[string]string{"region": "*", "gpu": "a10"})
	if err != nil {
		t.Fatal(err)
	}
	sec, err := ParseJoinToken(raw)
	if err != nil || sec.ID != tok.ID {
		t.Fatalf("parse: %v %+v", err, sec)
	}
	if err := tok.Redeem("wrong", now, nil); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong secret accepted: %v", err)
	}
	if err := tok.Redeem(sec.Secret, now, map[string]string{"gpu": "h100"}); !errors.Is(err, ErrLabelDenied) {
		t.Fatalf("disallowed label accepted: %v", err)
	}
	if err := tok.Redeem(sec.Secret, now, map[string]string{"region": "eu", "gpu": "a10"}); err != nil {
		t.Fatalf("valid redeem failed: %v", err)
	}
	if err := tok.Redeem(sec.Secret, now, nil); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("max uses not enforced")
	}
	tok2, raw2, _ := NewJoinToken("gpu", now, time.Minute, 0, nil)
	s2, _ := ParseJoinToken(raw2)
	if err := tok2.Redeem(s2.Secret, now.Add(2*time.Minute), nil); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("expiry not enforced")
	}
}

func TestSelector(t *testing.T) {
	p := Pool{Name: "gpu"}
	if !p.EffectiveSelector().Matches(map[string]string{PoolLabel: "gpu", "x": "y"}) {
		t.Fatal("default selector should match pool label")
	}
	if (Selector{"zone": "a"}).Matches(map[string]string{"zone": "b"}) {
		t.Fatal("mismatch should not match")
	}
}

func TestSupportsRuntime(t *testing.T) {
	w := Worker{Runtimes: []string{"python/3.12"}}
	if !w.SupportsRuntime("python") || w.SupportsRuntime("go") {
		t.Fatal("runtime matching broken")
	}
}
