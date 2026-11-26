package usecase

import (
	"context"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/app/scheduler"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

// Assignment is a work item bound to a worker, with everything it needs.
type Assignment struct {
	Item     *task.TaskRun
	Run      *run.Run
	Workflow workflow.Workflow
	// History is set for workflow steps.
	History []run.HistoryEvent
}

// Poll long-polls for up to max work items the worker can run.
func (s *Service) Poll(ctx context.Context, w *worker.Worker, max int, wait time.Duration) ([]Assignment, error) {
	if max <= 0 {
		return nil, nil
	}
	if wait > s.cfg.MaxPollWait {
		wait = s.cfg.MaxPollWait
	}
	work, cancel := s.notifier.Subscribe(ports.TopicWork)
	defer cancel()
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	// Retries become due without a notification; re-check periodically.
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		out, err := s.tryBind(ctx, w.Name, max)
		if err != nil || len(out) > 0 {
			return out, err
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-deadline.C:
			return nil, nil
		case <-work:
		case <-tick.C:
		}
	}
}

func (s *Service) tryBind(ctx context.Context, workerName string, max int) ([]Assignment, error) {
	var out []Assignment
	err := s.tx(ctx, func(t *txn) error {
		w, err := t.Workers().Get(ctx, workerName)
		if err != nil {
			return ErrUnauthenticated
		}
		if !w.Schedulable() {
			return nil
		}
		// Only this worker's tenants: a worker never sees (nor is slowed
		// down by) the backlog of a namespace it may not serve.
		served, err := s.servedNamespaces(ctx, t, w)
		if err != nil {
			return err
		}
		pending, err := t.Tasks().ListPending(ctx, t.now, 256, served)
		if err != nil || len(pending) == 0 {
			return err
		}
		node, err := t.nodeInfo(w)
		if err != nil {
			return err
		}
		node.AllNamespaces = served == nil
		for _, n := range served {
			node.Namespaces[n] = true
		}
		runs := map[string]*run.Run{}
		wfs := map[string]workflow.Workflow{}
		for _, item := range pending {
			if len(out) >= max {
				break
			}
			r, ok := runs[item.RunID]
			if !ok {
				if r, err = t.Runs().GetByID(ctx, item.RunID); err != nil {
					return err
				}
				runs[item.RunID] = r
			}
			wkey := r.Namespace + "/" + r.Workflow + "@" + r.Version
			wf, ok := wfs[wkey]
			if !ok {
				if wf, err = t.Workflows().Get(ctx, r.Namespace, r.Workflow, r.Version); err != nil {
					return err
				}
				wfs[wkey] = wf
			}
			cand := scheduler.Candidate{Item: item, Runtime: wf.Manifest.Runtime}
			if s.scheduler.Feasible(cand, node) != nil {
				continue
			}
			var timeout time.Duration
			if item.Kind == task.KindStep {
				timeout = 2 * time.Minute
			} else if spec, ok := wf.Manifest.Task(item.Name); ok {
				timeout = spec.Timeout()
			}
			if err := item.Bind(w.Name, s.ids.NewID("lease"), t.now, s.cfg.AckTTL, timeout); err != nil {
				return err
			}
			if item.Kind == task.KindStep {
				exp := t.now.Add(s.cfg.LeaseTTL)
				item.LeaseExpiresAt = &exp
			}
			if err := t.Tasks().Update(ctx, item); err != nil {
				return err
			}
			s.scheduler.Reserve(cand, node)
			a := Assignment{Item: item, Run: r, Workflow: wf}
			if item.Kind == task.KindStep {
				if a.History, err = t.History().List(ctx, r.ID); err != nil {
					return err
				}
			}
			out = append(out, a)
		}
		return nil
	})
	return out, err
}

func (t *txn) nodeInfo(w *worker.Worker) (*scheduler.NodeInfo, error) {
	active, err := t.Tasks().ListActiveByWorker(t.ctx, w.Name)
	if err != nil {
		return nil, err
	}
	var used workflow.Resources
	for _, it := range active {
		used = used.Add(it.Requests)
	}
	pools, err := t.Pools().List(t.ctx)
	if err != nil {
		return nil, err
	}
	n := &scheduler.NodeInfo{Worker: w, Used: used, Pools: map[string]*worker.Pool{}, PoolActive: map[string]int{}, Namespaces: map[string]bool{}}
	for _, p := range pools {
		n.Pools[p.Name] = p
		if p.MaxConcurrentTasks > 0 {
			c, err := t.Tasks().CountActiveInPool(t.ctx, p.Name)
			if err != nil {
				return nil, err
			}
			n.PoolActive[p.Name] = c
		}
	}
	return n, nil
}
