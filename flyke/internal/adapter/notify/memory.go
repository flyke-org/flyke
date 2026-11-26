// Package notify provides an in-process ports.Notifier. Multi-replica
// deployments would swap it for Postgres LISTEN/NOTIFY or NATS.
package notify

import "sync"

type Memory struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func NewMemory() *Memory { return &Memory{subs: map[string]map[chan struct{}]struct{}{}} }

func (m *Memory) Notify(topic string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subs[topic] {
		select {
		case ch <- struct{}{}:
		default: // already signalled: notifications coalesce
		}
	}
}

func (m *Memory) Subscribe(topic string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	m.mu.Lock()
	if m.subs[topic] == nil {
		m.subs[topic] = map[chan struct{}]struct{}{}
	}
	m.subs[topic][ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs[topic], ch)
		if len(m.subs[topic]) == 0 {
			delete(m.subs, topic)
		}
		m.mu.Unlock()
	}
}
