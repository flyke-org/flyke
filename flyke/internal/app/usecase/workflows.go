package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

// RegisterWorkflow validates a manifest and stores it as a new immutable
// version (no-op if the same content is already registered).
func (s *Service) RegisterWorkflow(ctx context.Context, ns, name string, rawManifest json.RawMessage, src workflow.Source) (workflow.Workflow, error) {
	var m workflow.Manifest
	if err := json.Unmarshal(rawManifest, &m); err != nil {
		return workflow.Workflow{}, invalid("manifest: %v", err)
	}
	if name != "" && m.Name != name {
		return workflow.Workflow{}, invalid("manifest name %q does not match %q", m.Name, name)
	}
	if err := m.Validate(); err != nil {
		return workflow.Workflow{}, invalid("%v", err)
	}
	if src.Entrypoint == "" {
		src.Entrypoint = m.Entrypoint
	}
	if (src.LocalRoot == "") == (src.BundleDigest == "") {
		return workflow.Workflow{}, invalid("source needs exactly one of localRoot or bundleDigest")
	}
	if _, err := compileSchema(m.InputsSchema); err != nil {
		return workflow.Workflow{}, invalid("inputs schema: %v", err)
	}
	wf := workflow.Workflow{
		Namespace: ns, Name: m.Name, Manifest: m, Source: src,
		Version: workflow.ComputeVersion(m, src),
	}
	err := s.tx(ctx, func(t *txn) error {
		if src.BundleDigest != "" {
			// A digest is not a capability: the namespace must have
			// uploaded the bundle itself (or the admin shared it).
			ok, err := t.Bundles().Usable(ctx, ns, src.BundleDigest)
			if err != nil {
				return err
			}
			rc, err := s.blobs.Get(ctx, src.BundleDigest)
			if err != nil || !ok {
				if err == nil {
					rc.Close()
				}
				return invalid("bundle %s not found; upload it first", src.BundleDigest)
			}
			rc.Close()
		}
		existing, err := t.Workflows().Get(ctx, ns, wf.Name, wf.Version)
		switch {
		case err == nil:
			wf = existing // already registered: bump to latest
		case errors.Is(err, ports.ErrNotFound):
			wf.CreatedAt = t.now
		default:
			return err
		}
		return t.activate(&wf)
	})
	return wf, err
}

// activate makes wf the current version: new runs, triggers and declared
// schedules use it from now on. Runs already started keep their version.
func (t *txn) activate(wf *workflow.Workflow) error {
	wf.ActivatedAt = t.now
	if err := t.Workflows().Put(t.ctx, *wf); err != nil {
		return err
	}
	// Schedules declared in code (on.schedule) follow the current version.
	return t.syncDeclaredSchedules(*wf)
}

// WorkflowVersion is one registered version of a workflow with what ran on it.
type WorkflowVersion struct {
	workflow.Workflow
	Current bool
	Runs    ports.VersionRunStats
	// BundleUploadedAt and BundleSize describe the code bundle, if any.
	BundleUploadedAt *time.Time
	BundleSize       int64
}

// ListWorkflowVersions returns every version of a workflow, current first.
func (s *Service) ListWorkflowVersions(ctx context.Context, ns, name string) ([]WorkflowVersion, error) {
	var out []WorkflowVersion
	err := s.read(ctx, func(r ports.Repos) error {
		wfs, err := r.Workflows().ListVersions(ctx, ns, name)
		if err != nil {
			return err
		}
		if len(wfs) == 0 {
			return fmt.Errorf("workflow %s: %w", name, ports.ErrNotFound)
		}
		stats, err := r.Runs().VersionStats(ctx, ns, name)
		if err != nil {
			return err
		}
		for i, wf := range wfs {
			v, err := s.describeVersion(ctx, r, wf, stats)
			if err != nil {
				return err
			}
			v.Current = i == 0
			out = append(out, v)
		}
		return nil
	})
	return out, err
}

// GetWorkflowVersion returns one version of a workflow.
func (s *Service) GetWorkflowVersion(ctx context.Context, ns, name, version string) (WorkflowVersion, error) {
	var out WorkflowVersion
	err := s.read(ctx, func(r ports.Repos) error {
		wf, err := r.Workflows().Get(ctx, ns, name, version)
		if err != nil {
			return fmt.Errorf("workflow %s version %s: %w", name, version, err)
		}
		cur, err := r.Workflows().GetLatest(ctx, ns, name)
		if err != nil {
			return err
		}
		stats, err := r.Runs().VersionStats(ctx, ns, name)
		if err != nil {
			return err
		}
		out, err = s.describeVersion(ctx, r, wf, stats)
		out.Current = cur.Version == wf.Version
		return err
	})
	return out, err
}

func (s *Service) describeVersion(ctx context.Context, r ports.Repos, wf workflow.Workflow, stats map[string]*ports.VersionRunStats) (WorkflowVersion, error) {
	v := WorkflowVersion{Workflow: wf, Runs: ports.VersionRunStats{Phases: map[run.Phase]int{}}}
	if st := stats[wf.Version]; st != nil {
		v.Runs = *st
	}
	if d := wf.Source.BundleDigest; d != "" {
		at, err := r.Bundles().UploadedAt(ctx, wf.Namespace, d)
		if err != nil {
			return v, err
		}
		v.BundleUploadedAt = at
		if sz, ok := s.blobs.(interface {
			Size(ctx context.Context, digest string) (int64, error)
		}); ok {
			v.BundleSize, _ = sz.Size(ctx, d)
		}
	}
	return v, nil
}

// ActivateWorkflowVersion makes a previously registered version current
// again (a rollback, or a roll forward after one).
func (s *Service) ActivateWorkflowVersion(ctx context.Context, ns, name, version string) (workflow.Workflow, error) {
	var wf workflow.Workflow
	err := s.tx(ctx, func(t *txn) error {
		var err error
		wf, err = t.Workflows().Get(ctx, ns, name, version)
		if err != nil {
			return fmt.Errorf("workflow %s version %s: %w", name, version, err)
		}
		if d := wf.Source.BundleDigest; d != "" {
			ok, err := t.Bundles().Usable(ctx, ns, d)
			if err != nil {
				return err
			}
			if !ok {
				return precondition("bundle %s of version %s is no longer available", d, version)
			}
		}
		return t.activate(&wf)
	})
	return wf, err
}

// workflowVersion resolves the version a new run uses: the current one, or
// an explicit (possibly older) one.
func (t *txn) workflowVersion(ns, name, version string) (workflow.Workflow, error) {
	if version == "" {
		wf, err := t.Workflows().GetLatest(t.ctx, ns, name)
		if err != nil {
			return wf, fmt.Errorf("workflow %s: %w", name, err)
		}
		return wf, nil
	}
	wf, err := t.Workflows().Get(t.ctx, ns, name, version)
	if err != nil {
		return wf, fmt.Errorf("workflow %s version %s: %w", name, version, err)
	}
	return wf, nil
}

func (s *Service) GetWorkflow(ctx context.Context, ns, name string) (wf workflow.Workflow, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		wf, err = r.Workflows().GetLatest(ctx, ns, name)
		return err
	})
	return
}

func (s *Service) ListWorkflows(ctx context.Context, ns string) (out []workflow.Workflow, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		out, err = r.Workflows().ListLatest(ctx, ns)
		return err
	})
	return
}

// UploadBundle stores a code bundle (tar.gz) for ns and returns its digest.
// ns "" (the admin) makes it usable by every namespace.
func (s *Service) UploadBundle(ctx context.Context, ns string, r io.Reader) (string, int64, error) {
	digest, size, err := s.blobs.Put(ctx, r)
	if err != nil {
		return "", 0, err
	}
	err = s.tx(ctx, func(t *txn) error { return t.Bundles().AddOwner(ctx, ns, digest, t.now) })
	return digest, size, err
}

// OpenBundle returns a bundle's content to worker w, which may only fetch
// code of the tenants it serves.
func (s *Service) OpenBundle(ctx context.Context, w *worker.Worker, digest string) (io.ReadCloser, error) {
	if w.Namespace != "" {
		var ok bool
		err := s.read(ctx, func(r ports.Repos) (err error) {
			ok, err = r.Bundles().Usable(ctx, w.Namespace, digest)
			return err
		})
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: bundle %s", ErrNotFound, digest)
		}
	}
	rc, err := s.blobs.Get(ctx, digest)
	if err != nil {
		return nil, fmt.Errorf("%w: bundle %s", ErrNotFound, digest)
	}
	return rc, nil
}

// ------------------------------------------------------------- schemas ----

type compiledSchema struct {
	schema   *jsonschema.Schema
	defaults map[string]any
}

func compileSchema(raw json.RawMessage) (*compiledSchema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("inputs.json", doc); err != nil {
		return nil, err
	}
	sch, err := c.Compile("inputs.json")
	if err != nil {
		return nil, err
	}
	var shape struct {
		Properties map[string]struct {
			Default any `json:"default"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(raw, &shape)
	defaults := map[string]any{}
	for k, p := range shape.Properties {
		if p.Default != nil {
			defaults[k] = p.Default
		}
	}
	return &compiledSchema{schema: sch, defaults: defaults}, nil
}

func (s *Service) schemaFor(wf workflow.Workflow) (*compiledSchema, error) {
	if v, ok := s.schemas.Load(wf.Version); ok {
		return v.(*compiledSchema), nil
	}
	c, err := compileSchema(wf.Manifest.InputsSchema)
	if err != nil {
		return nil, err
	}
	s.schemas.Store(wf.Version, c)
	return c, nil
}

// prepareInputs applies schema defaults and validates the result.
func (s *Service) prepareInputs(wf workflow.Workflow, raw json.RawMessage) (map[string]any, json.RawMessage, error) {
	inputs := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		if err := json.Unmarshal(raw, &inputs); err != nil {
			return nil, nil, invalid("inputs must be a JSON object: %v", err)
		}
	}
	sch, err := s.schemaFor(wf)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range sch.defaults {
		if _, ok := inputs[k]; !ok {
			inputs[k] = v
		}
	}
	// Round-trip through JSON so the validator sees canonical types.
	b, _ := json.Marshal(inputs)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, nil, invalid("inputs: %v", err)
	}
	if err := sch.schema.Validate(doc); err != nil {
		return nil, nil, invalid("inputs do not match the workflow schema: %v", err)
	}
	return inputs, b, nil
}
