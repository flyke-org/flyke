// Package controller runs the control plane's reconcile loops.
//
// Like kube-controller-manager, each controller is level-triggered: it is
// woken by notifications (fast path) and by a periodic resync (safety net),
// and each pass converges current state towards desired state.
package controller

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
)

// Reconciler performs one convergence pass.
type Reconciler struct {
	Name      string
	Reconcile func(ctx context.Context) error
	// Resync is the maximum time between two passes.
	Resync time.Duration
	// Wake makes the reconciler react to reconcile notifications.
	Wake bool

	// Plan, when set, replaces Reconcile for a timer-driven controller: it
	// performs one pass and returns how long until the next one is needed
	// (0 = nothing planned). The loop then sleeps that long, at most Resync,
	// instead of polling. Topic is the notification that wakes it early.
	Plan  func(ctx context.Context) (time.Duration, error)
	Topic string
}

type Manager struct {
	notifier    ports.Notifier
	reconcilers []Reconciler
	log         *slog.Logger
}

func NewManager(n ports.Notifier, log *slog.Logger, rs ...Reconciler) *Manager {
	return &Manager{notifier: n, reconcilers: rs, log: log}
}

// Run blocks until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, r := range m.reconcilers {
		wg.Add(1)
		go func(r Reconciler) {
			defer wg.Done()
			m.loop(ctx, r)
		}(r)
	}
	wg.Wait()
}

func (m *Manager) loop(ctx context.Context, r Reconciler) {
	if r.Plan != nil {
		m.planLoop(ctx, r)
		return
	}
	var wake <-chan struct{}
	if r.Wake {
		ch, cancel := m.notifier.Subscribe(ports.TopicReconcile)
		defer cancel()
		wake = ch
	}
	ticker := time.NewTicker(r.Resync)
	defer ticker.Stop()
	for {
		if err := r.Reconcile(ctx); err != nil && ctx.Err() == nil {
			m.log.Warn("reconcile failed", "controller", r.Name, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-ticker.C:
		}
	}
}

// planLoop runs a timer-driven controller: it sleeps until the time its last
// pass asked for, so an idle controller costs nothing between two passes.
// Resync bounds the sleep, which covers clock jumps and suspended hosts.
func (m *Manager) planLoop(ctx context.Context, r Reconciler) {
	wake, cancel := m.notifier.Subscribe(r.Topic)
	defer cancel()
	timer := time.NewTimer(r.Resync)
	defer timer.Stop()
	for {
		wait, err := r.Plan(ctx)
		if err != nil && ctx.Err() == nil {
			m.log.Warn("reconcile failed", "controller", r.Name, "err", err)
		}
		switch {
		case err != nil:
			wait = min(5*time.Second, r.Resync) // retry soon
		case wait <= 0 || wait > r.Resync:
			wait = r.Resync
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		}
	}
}
