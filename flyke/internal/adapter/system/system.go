// Package system provides the real clock and id generator.
package system

import (
	"crypto/rand"
	"time"
)

type Clock struct{}

func (Clock) Now() time.Time { return time.Now() }

type IDs struct{}

const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// NewID returns "<prefix>-<8 random chars>" (≈41 bits of entropy).
func (IDs) NewID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	if prefix == "" {
		return string(b)
	}
	return prefix + "-" + string(b)
}
