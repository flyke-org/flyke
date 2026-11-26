// Package scheduler decides which work items a worker may run, following the
// kube-scheduler framework: a chain of filter plugins, each able to veto a
// (work item, worker) pair with a reason.
//
// Flyke workers pull work (long-poll), so scheduling happens per poll: the
// polling worker is the node, and pending items are filtered against it.
package scheduler

import (
	"fmt"

	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

// NodeInfo is the scheduling view of a worker.
type NodeInfo struct {
	Worker *worker.Worker
	// Used is the sum of requests of items currently bound to the worker.
	Used  workflow.Resources
	Pools map[string]*worker.Pool
	// PoolActive counts active items per pool (for pool slot limits).
	PoolActive map[string]int
	// Namespaces are the tenants whose work the worker may run: its own
	// namespace, or for a shared worker the namespaces allowed to use shared
	// workers. AllNamespaces lifts the restriction (dev control planes).
	Namespaces    map[string]bool
	AllNamespaces bool
}

// Candidate is a work item with the context needed to place it.
type Candidate struct {
	Item    *task.TaskRun
	Runtime string
}

type FilterPlugin interface {
	Name() string
	// Filter returns nil if the candidate may run on the node.
	Filter(c Candidate, n *NodeInfo) error
}

type Scheduler struct{ filters []FilterPlugin }

func New(filters ...FilterPlugin) *Scheduler { return &Scheduler{filters: filters} }

// Feasible runs all filters; the error names the first plugin that vetoed.
func (s *Scheduler) Feasible(c Candidate, n *NodeInfo) error {
	for _, f := range s.filters {
		if err := f.Filter(c, n); err != nil {
			return fmt.Errorf("%s: %w", f.Name(), err)
		}
	}
	return nil
}

// Reserve accounts a bound item on the node so later candidates in the same
// poll see the updated usage.
func (s *Scheduler) Reserve(c Candidate, n *NodeInfo) {
	n.Used = n.Used.Add(c.Item.Requests)
	if c.Item.Pool != "" {
		n.PoolActive[c.Item.Pool]++
	}
}

func DefaultFilters(dev bool) []FilterPlugin {
	return []FilterPlugin{schedulable{}, tenantMatch{}, runtimeMatch{}, poolSelector{}, poolSlots{}, resourcesFit{}}
}

// tenantMatch is the isolation boundary of the data plane: a tenant's code
// and secrets only ever reach a worker that serves that tenant.
type tenantMatch struct{}

func (tenantMatch) Name() string { return "TenantMatch" }
func (tenantMatch) Filter(c Candidate, n *NodeInfo) error {
	if n.AllNamespaces || n.Namespaces[c.Item.Namespace] {
		return nil
	}
	return fmt.Errorf("worker %s does not serve namespace %s", n.Worker.Name, c.Item.Namespace)
}

type schedulable struct{}

func (schedulable) Name() string { return "Schedulable" }
func (schedulable) Filter(_ Candidate, n *NodeInfo) error {
	if !n.Worker.Schedulable() {
		return fmt.Errorf("worker %s is not schedulable", n.Worker.Name)
	}
	return nil
}

type runtimeMatch struct{}

func (runtimeMatch) Name() string { return "RuntimeMatch" }
func (runtimeMatch) Filter(c Candidate, n *NodeInfo) error {
	if !n.Worker.SupportsRuntime(c.Runtime) {
		return fmt.Errorf("runtime %s not supported", c.Runtime)
	}
	return nil
}

type poolSelector struct{}

func (poolSelector) Name() string { return "PoolSelector" }
func (poolSelector) Filter(c Candidate, n *NodeInfo) error {
	if c.Item.Pool == "" {
		return nil
	}
	p, ok := n.Pools[c.Item.Pool]
	if !ok {
		return fmt.Errorf("pool %s does not exist", c.Item.Pool)
	}
	if p.Namespace != "" && p.Namespace != c.Item.Namespace {
		// Another tenant's pool: never consume its slots.
		return fmt.Errorf("pool %s does not exist", c.Item.Pool)
	}
	if !p.EffectiveSelector().Matches(n.Worker.Labels) {
		return fmt.Errorf("worker not in pool %s", c.Item.Pool)
	}
	return nil
}

type poolSlots struct{}

func (poolSlots) Name() string { return "PoolSlots" }
func (poolSlots) Filter(c Candidate, n *NodeInfo) error {
	if c.Item.Pool == "" {
		return nil
	}
	p := n.Pools[c.Item.Pool]
	if p != nil && p.MaxConcurrentTasks > 0 && n.PoolActive[p.Name] >= p.MaxConcurrentTasks {
		return fmt.Errorf("pool %s is full", p.Name)
	}
	return nil
}

type resourcesFit struct{}

func (resourcesFit) Name() string { return "ResourcesFit" }
func (resourcesFit) Filter(c Candidate, n *NodeInfo) error {
	capacity := n.Worker.Capacity
	if capacity.CPU == 0 && capacity.MemoryBytes == 0 {
		return nil // worker did not advertise capacity: slots only
	}
	free := capacity.Sub(n.Used)
	if !c.Item.Requests.Fits(free) {
		return fmt.Errorf("insufficient resources (requested cpu=%.2f mem=%d, free cpu=%.2f mem=%d)",
			c.Item.Requests.CPU, c.Item.Requests.MemoryBytes, free.CPU, free.MemoryBytes)
	}
	return nil
}
