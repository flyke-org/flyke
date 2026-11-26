package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
)

func TestWorkflowVersionsAndRollback(t *testing.T) {
	h := start(t)
	const base = "/api/v1/namespaces/default/workflows/Pipeline"
	var first v1.Workflow
	h.rest(http.MethodGet, base, nil, &first)

	// A second deploy from other code becomes current.
	var second v1.Workflow
	if c := h.rest(http.MethodPut, base, v1.WorkflowSpec{Manifest: json.RawMessage(manifest), Source: v1.Source{LocalRoot: "/code-v2"}}, &second); c != http.StatusOK {
		t.Fatalf("register v2: %d", c)
	}
	if second.Status.Version == first.Status.Version {
		t.Fatal("different source must give a different version")
	}

	start := func(version string) v1.Run {
		var r v1.Run
		if c := h.rest(http.MethodPost, base+"/runs", v1.StartRunRequest{Version: version, Inputs: json.RawMessage(`{"key":"` + version + `x"}`)}, &r); c != http.StatusCreated {
			t.Fatalf("start run (version %q): %d", version, c)
		}
		return r
	}
	if r := start(""); r.Spec.Version != second.Status.Version {
		t.Fatalf("default run used %s, want current %s", r.Spec.Version, second.Status.Version)
	}
	if r := start(first.Status.Version); r.Spec.Version != first.Status.Version {
		t.Fatalf("pinned run used %s, want %s", r.Spec.Version, first.Status.Version)
	}
	start(first.Status.Version)

	var versions v1.List[v1.WorkflowVersion]
	h.rest(http.MethodGet, base+"/versions", nil, &versions)
	if len(versions.Items) != 2 {
		t.Fatalf("got %d versions, want 2", len(versions.Items))
	}
	cur, old := versions.Items[0], versions.Items[1]
	if !cur.Current || cur.Version != second.Status.Version || old.Current {
		t.Fatalf("current must come first: %+v", versions.Items)
	}
	if cur.Runs.Total != 1 || old.Runs.Total != 2 || old.Runs.LastRunAt == nil {
		t.Fatalf("run counts: current %d, old %d", cur.Runs.Total, old.Runs.Total)
	}

	var runs v1.List[v1.Run]
	h.rest(http.MethodGet, "/api/v1/namespaces/default/runs?workflow=Pipeline&version="+first.Status.Version, nil, &runs)
	if len(runs.Items) != 2 {
		t.Fatalf("runs of the old version: %d, want 2", len(runs.Items))
	}

	// Roll back.
	var rolled v1.Workflow
	if c := h.rest(http.MethodPost, base+"/versions/"+first.Status.Version+"/activate", nil, &rolled); c != http.StatusOK {
		t.Fatalf("activate: %d", c)
	}
	var now v1.Workflow
	h.rest(http.MethodGet, base, nil, &now)
	if now.Status.Version != first.Status.Version {
		t.Fatalf("current after rollback is %s, want %s", now.Status.Version, first.Status.Version)
	}
	var v v1.WorkflowVersion
	h.rest(http.MethodGet, base+"/versions/"+first.Status.Version, nil, &v)
	if !v.Current || !v.ActivatedAt.After(v.Metadata.CreatedAt) {
		t.Fatalf("rolled back version: current=%v activated=%v created=%v", v.Current, v.ActivatedAt, v.Metadata.CreatedAt)
	}

	if c := h.rest(http.MethodPost, base+"/versions/v-000000000000/activate", nil, nil); c != http.StatusNotFound {
		t.Fatalf("activating an unknown version: %d, want 404", c)
	}
	if c := h.rest(http.MethodPost, base+"/runs", v1.StartRunRequest{Version: "v-000000000000", Inputs: json.RawMessage(`{"key":"k"}`)}, nil); c != http.StatusNotFound {
		t.Fatalf("running an unknown version: %d, want 404", c)
	}
}
