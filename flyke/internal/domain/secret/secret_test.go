package secret

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	s := Secret{Namespace: "default", Key: "infoblox_password", Value: "x"}
	if err := s.Normalize(); err != nil || s.Type != Private || s.Namespace != "default" {
		t.Fatalf("defaults: %v %+v", err, s)
	}
	sh := Secret{Namespace: "team-a", Key: "API_URL", Type: Shared}
	if err := sh.Normalize(); err != nil || sh.Namespace != SharedNamespace {
		t.Fatalf("shared: %v %+v", err, sh)
	}
	for _, bad := range []Secret{
		{Namespace: "default", Key: "1abc"},
		{Namespace: "default", Key: "has space"},
		{Namespace: "default", Key: "k", Type: "Global"},
		{Key: "k", Type: Private},
		{Namespace: "default", Key: "k", Value: strings.Repeat("x", MaxValueBytes+1)},
	} {
		if err := bad.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: want ErrInvalid, got %v", bad, err)
		}
	}
}

func TestRedacted(t *testing.T) {
	if got := (Secret{Value: "v", Sensitive: true}).Redacted(); got.Value != "" {
		t.Fatal("sensitive value not redacted")
	}
	if got := (Secret{Value: "v"}).Redacted(); got.Value != "v" {
		t.Fatal("plain variable redacted")
	}
}
