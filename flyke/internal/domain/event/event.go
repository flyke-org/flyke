// Package event models named, structured facts posted to the event bus.
package event

import (
	"encoding/json"
	"time"
)

type Event struct {
	ID        string
	Namespace string
	Name      string
	Payload   json.RawMessage
	// Source is "api", "workflow:<run id>" or "trigger".
	Source    string
	CreatedAt time.Time
}

// Fields decodes the payload as an object (empty on error / non-object).
func (e Event) Fields() map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(e.Payload, &m)
	return m
}
