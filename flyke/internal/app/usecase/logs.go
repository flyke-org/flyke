package usecase

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
)

type LogRecord struct {
	RunID   string
	WorkID  string
	Attempt int
	Seq     int64
	Time    time.Time
	Level   string
	Message string
	Fields  json.RawMessage
	Stream  string
}

// PushLogs ingests log records from a worker. Records for work items the
// worker was never assigned are dropped (a worker can only write its own logs).
func (s *Service) PushLogs(ctx context.Context, w *worker.Worker, recs []LogRecord) (int, error) {
	var accepted int
	err := s.tx(ctx, func(t *txn) error {
		items := map[string]*task.TaskRun{}
		var entries []ports.LogEntry
		runs := map[string]struct{}{}
		for _, rec := range recs {
			item, ok := items[rec.WorkID]
			if !ok {
				it, err := t.Tasks().Get(ctx, rec.WorkID)
				if err != nil {
					items[rec.WorkID] = nil
					continue
				}
				item, items[rec.WorkID] = it, it
			}
			if item == nil || item.Worker != w.Name || item.RunID != rec.RunID {
				continue
			}
			source := "worker"
			if item.Kind == task.KindStep {
				source = "workflow"
			}
			entries = append(entries, ports.LogEntry{
				Namespace: item.Namespace, RunID: item.RunID, WorkID: item.ID,
				Task: item.Name, Ref: item.Ref, Attempt: rec.Attempt, Worker: w.Name,
				Seq: rec.Seq, Time: rec.Time, Level: rec.Level, Source: source,
				Stream: rec.Stream, Message: rec.Message, Fields: rec.Fields,
			})
			runs[item.RunID] = struct{}{}
		}
		n, err := t.Logs().Append(ctx, entries...)
		accepted = n
		for id := range runs {
			t.notify(ports.TopicRun(id))
		}
		return err
	})
	return accepted, err
}

func (s *Service) ListLogs(ctx context.Context, ns, runID string, after int64, limit int) (out []ports.LogEntry, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		if _, err := r.Runs().Get(ctx, ns, runID); err != nil {
			return err
		}
		out, err = r.Logs().List(ctx, runID, after, limit)
		return err
	})
	return
}

// FollowLogs streams log entries to emit until the run is terminal and all
// logs are delivered, or ctx ends. after is the last id already seen.
// idle is called about once per second while nothing new arrives.
func (s *Service) FollowLogs(ctx context.Context, ns, runID string, after int64, emit func(ports.LogEntry) error, idle func() error) error {
	changed, cancel := s.notifier.Subscribe(ports.TopicRun(runID))
	defer cancel()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	drained := 0
	for {
		r, err := s.GetRun(ctx, ns, runID)
		if err != nil {
			return err
		}
		entries, err := s.ListLogs(ctx, ns, runID, after, 500)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := emit(e); err != nil {
				return err
			}
			after = e.ID
		}
		if len(entries) == 500 {
			continue
		}
		if r.Phase.Terminal() {
			// Give late worker log batches a moment to arrive.
			if drained++; drained > 2 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		case <-tick.C:
			if idle != nil {
				if err := idle(); err != nil {
					return err
				}
			}
		}
	}
}
