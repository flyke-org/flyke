package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
	"github.com/flyke-org/flyke/flyke/server"
)

// fakeDiscover reports one workflow per file named "<x>.py" containing "ok".
func fakeDiscover(_ context.Context, root string) (json.RawMessage, error) {
	d := v1.PlaygroundDiscovery{Workflows: []v1.PlaygroundWorkflow{}, Errors: []v1.PlaygroundError{}}
	b, err := os.ReadFile(filepath.Join(root, "p.py"))
	switch {
	case err != nil:
	case string(b) == "ok":
		d.Workflows = append(d.Workflows, v1.PlaygroundWorkflow{Entrypoint: "p.py:Pipeline", Manifest: json.RawMessage(manifest)})
	default:
		d.Errors = append(d.Errors, v1.PlaygroundError{File: "p.py", Line: 1, Error: "SyntaxError: invalid syntax"})
	}
	return json.Marshal(d)
}

func startPlayground(t *testing.T, dev bool) *harness {
	t.Helper()
	dir := t.TempDir()
	srv, err := server.New(server.Config{
		Addr: "127.0.0.1:0", DataDir: dir, Dev: dev, ResyncInterval: 50 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Discover: fakeDiscover,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return &harness{t: t, srv: srv}
}

func TestPlaygroundDisabledOutsideDev(t *testing.T) {
	h := startPlayground(t, false)
	var info v1.PlaygroundInfo
	h.rest(http.MethodGet, "/api/v1/playground", nil, &info)
	if info.Enabled || info.Reason == "" {
		t.Fatalf("playground must be disabled on non-dev servers: %+v", info)
	}
	if code := h.rest(http.MethodPut, "/api/v1/playground/projects/demo", v1.PlaygroundProjectSpec{Files: map[string]string{"p.py": "ok"}}, nil); code != http.StatusNotFound {
		t.Fatalf("PUT on non-dev server = %d, want 404", code)
	}
}

func TestPlaygroundSaveDiscoverRun(t *testing.T) {
	h := startPlayground(t, true)
	var info v1.PlaygroundInfo
	h.rest(http.MethodGet, "/api/v1/playground", nil, &info)
	if !info.Enabled {
		t.Fatal("playground should be enabled on dev servers")
	}

	// Path traversal and hidden files are rejected.
	for _, bad := range []string{"../x.py", "/abs.py", ".env", "a/../b.py", "__pycache__/x.pyc"} {
		if code := h.rest(http.MethodPut, "/api/v1/playground/projects/demo", v1.PlaygroundProjectSpec{Files: map[string]string{bad: "x"}}, nil); code != http.StatusUnprocessableEntity {
			t.Fatalf("file %q: status %d, want 422", bad, code)
		}
	}

	var p v1.PlaygroundProject
	h.rest(http.MethodPut, "/api/v1/playground/projects/demo", v1.PlaygroundProjectSpec{Files: map[string]string{"p.py": "broken", "lib/util.py": "x"}}, &p)
	if len(p.Discovery.Errors) != 1 || len(p.Discovery.Workflows) != 0 {
		t.Fatalf("discovery = %+v, want one error", p.Discovery)
	}
	h.rest(http.MethodPut, "/api/v1/playground/projects/demo", v1.PlaygroundProjectSpec{Files: map[string]string{"p.py": "ok"}}, &p)
	if len(p.Discovery.Workflows) != 1 {
		t.Fatalf("discovery = %+v, want one workflow", p.Discovery)
	}

	var got v1.PlaygroundProject
	h.rest(http.MethodGet, "/api/v1/playground/projects/demo", nil, &got)
	if len(got.Files) != 1 || got.Files["p.py"] != "ok" {
		t.Fatalf("files = %v, want only p.py (lib/util.py removed)", got.Files)
	}

	var list v1.List[v1.PlaygroundProject]
	h.rest(http.MethodGet, "/api/v1/playground/projects", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "demo" {
		t.Fatalf("projects = %+v", list.Items)
	}

	var r v1.Run
	code := h.rest(http.MethodPost, "/api/v1/namespaces/default/playground/projects/demo/runs",
		v1.PlaygroundRunRequest{Entrypoint: "p.py:Pipeline", Inputs: json.RawMessage(`{"key":"a"}`)}, &r)
	if code != http.StatusCreated || r.Spec.Workflow != "Pipeline" || r.Spec.TriggeredBy != "playground" {
		t.Fatalf("run: %d %+v", code, r.Spec)
	}
	var wf v1.Workflow
	h.rest(http.MethodGet, "/api/v1/namespaces/default/workflows/Pipeline", nil, &wf)
	if filepath.Base(wf.Spec.Source.LocalRoot) != "demo" {
		t.Fatalf("workflow source = %+v, want the project directory", wf.Spec.Source)
	}
	if code := h.rest(http.MethodPost, "/api/v1/namespaces/default/playground/projects/demo/runs",
		v1.PlaygroundRunRequest{Entrypoint: "p.py:Nope"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown entrypoint: %d, want 422", code)
	}

	if code := h.rest(http.MethodDelete, "/api/v1/playground/projects/demo", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code := h.rest(http.MethodGet, "/api/v1/playground/projects/demo", nil, nil); code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", code)
	}
}
