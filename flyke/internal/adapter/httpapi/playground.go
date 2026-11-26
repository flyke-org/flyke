package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
	"github.com/flyke-org/flyke/flyke/internal/app/usecase"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

// Playground lets the UI edit workflow code and run it. It is dev-only:
// saving a project imports its Python files on the control plane's machine,
// and runs read the code from the project directory (Source.LocalRoot), so the
// worker must share the filesystem (the local dev worker does).
type Playground struct {
	// Dir holds one directory per project.
	Dir string
	// Discover imports the code under root and returns the SDK's
	// `python -m flyke discover` output.
	Discover func(ctx context.Context, root string) (json.RawMessage, error)
}

const (
	maxPlaygroundFiles = 200
	maxPlaygroundBytes = 4 << 20
)

var (
	projectNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	filePartRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	// Files the SDK or Python may create next to the code; not project files.
	playgroundSkip = map[string]bool{"__pycache__": true}
)

type playground struct {
	cfg *Playground
	mu  sync.Mutex // serializes writes and discovery per control plane
}

func (a *API) routePlayground(mux *http.ServeMux, p, nsp string) {
	// The playground runs code on the control plane's host: admin only.
	mux.HandleFunc("GET "+p+"/playground", a.guard(anyAccess, a.playgroundInfo))
	mux.HandleFunc("GET "+p+"/playground/projects", a.guard(adminAccess, a.pgOnly(a.listProjects)))
	mux.HandleFunc("GET "+p+"/playground/projects/{project}", a.guard(adminAccess, a.pgOnly(a.getProject)))
	mux.HandleFunc("PUT "+p+"/playground/projects/{project}", a.guard(adminAccess, a.pgOnly(a.putProject)))
	mux.HandleFunc("DELETE "+p+"/playground/projects/{project}", a.guard(adminAccess, a.pgOnly(a.deleteProject)))
	mux.HandleFunc("POST "+nsp+"/playground/projects/{project}/runs", a.guard(adminAccess, a.pgOnly(a.runProject)))
}

func (a *API) playgroundInfo(w http.ResponseWriter, _ *http.Request) {
	info := v1.PlaygroundInfo{TypeMeta: tm("PlaygroundInfo"), Enabled: a.pg != nil}
	if a.pg == nil {
		info.Reason = "the playground runs your code on the control plane's machine, so it is only enabled on dev control planes: start one with `flyke server --dev`"
	}
	writeJSON(w, http.StatusOK, info)
}

func (a *API) pgOnly(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.pg == nil {
			writeStatus(w, http.StatusNotFound, "NotFound", "the playground is only available on dev control planes (`flyke server --dev`)")
			return
		}
		name := r.PathValue("project")
		if name != "" && !projectNameRe.MatchString(name) {
			writeStatus(w, http.StatusUnprocessableEntity, "Invalid", "project names are lowercase letters, digits, '-' and '_' (max 63)")
			return
		}
		h(w, r, name)
	}
}

func (pg *playground) dir(name string) string { return filepath.Join(pg.cfg.Dir, name) }

func (a *API) listProjects(w http.ResponseWriter, _ *http.Request, _ string) {
	entries, err := os.ReadDir(a.pg.cfg.Dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		a.fail(w, err)
		return
	}
	items := []v1.PlaygroundProject{}
	for _, e := range entries {
		if !e.IsDir() || !projectNameRe.MatchString(e.Name()) {
			continue
		}
		p := v1.PlaygroundProject{TypeMeta: tm("PlaygroundProject"), Name: e.Name()}
		if info, err := e.Info(); err == nil {
			t := info.ModTime().UTC()
			p.UpdatedAt = &t
		}
		items = append(items, p)
	}
	writeJSON(w, http.StatusOK, v1.List[v1.PlaygroundProject]{TypeMeta: tm("PlaygroundProjectList"), Items: items})
}

func (a *API) getProject(w http.ResponseWriter, r *http.Request, name string) {
	a.pg.mu.Lock()
	defer a.pg.mu.Unlock()
	files, err := readProject(a.pg.dir(name))
	if errors.Is(err, fs.ErrNotExist) {
		a.fail(w, fmt.Errorf("%w: playground project %s", usecase.ErrNotFound, name))
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	a.writeProject(w, r.Context(), http.StatusOK, name, files)
}

func (a *API) putProject(w http.ResponseWriter, r *http.Request, name string) {
	var body v1.PlaygroundProjectSpec
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	if err := validateFiles(body.Files); err != nil {
		a.fail(w, err)
		return
	}
	a.pg.mu.Lock()
	defer a.pg.mu.Unlock()
	if err := writeProjectFiles(a.pg.dir(name), body.Files); err != nil {
		a.fail(w, err)
		return
	}
	a.writeProject(w, r.Context(), http.StatusOK, name, body.Files)
}

func (a *API) deleteProject(w http.ResponseWriter, _ *http.Request, name string) {
	a.pg.mu.Lock()
	defer a.pg.mu.Unlock()
	if err := os.RemoveAll(a.pg.dir(name)); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runProject registers the workflow from the code as saved and starts a run.
func (a *API) runProject(w http.ResponseWriter, r *http.Request, name string) {
	var body v1.PlaygroundRunRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	a.pg.mu.Lock()
	dir := a.pg.dir(name)
	if _, err := os.Stat(dir); err != nil {
		a.pg.mu.Unlock()
		a.fail(w, fmt.Errorf("%w: playground project %s", usecase.ErrNotFound, name))
		return
	}
	d, err := a.discover(r.Context(), dir)
	a.pg.mu.Unlock()
	if err != nil {
		a.fail(w, err)
		return
	}
	var manifest json.RawMessage
	for _, wf := range d.Workflows {
		if wf.Entrypoint == body.Entrypoint {
			manifest = wf.Manifest
		}
	}
	if manifest == nil {
		msg := fmt.Sprintf("no workflow at %q in project %s", body.Entrypoint, name)
		for _, e := range d.Errors {
			msg += fmt.Sprintf("; %s: %s", e.File, e.Error)
		}
		a.fail(w, fmt.Errorf("%w: %s", usecase.ErrInvalidArgument, msg))
		return
	}
	ns := r.PathValue("ns")
	wf, err := a.svc.RegisterWorkflow(r.Context(), ns, "", manifest,
		workflow.Source{Entrypoint: body.Entrypoint, LocalRoot: dir})
	if err != nil {
		a.fail(w, err)
		return
	}
	x, err := a.svc.StartRun(r.Context(), usecase.StartRunInput{
		Namespace: ns, Workflow: wf.Name, Inputs: body.Inputs,
		Labels: map[string]string{"playground": name}, TriggeredBy: "playground",
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, runDTO(x))
}

func (a *API) writeProject(w http.ResponseWriter, ctx context.Context, code int, name string, files map[string]string) {
	d, err := a.discover(ctx, a.pg.dir(name))
	if err != nil {
		a.fail(w, err)
		return
	}
	p := v1.PlaygroundProject{TypeMeta: tm("PlaygroundProject"), Name: name, Files: files, Discovery: &d}
	if info, err := os.Stat(a.pg.dir(name)); err == nil {
		t := info.ModTime().UTC()
		p.UpdatedAt = &t
	}
	writeJSON(w, code, p)
}

func (a *API) discover(ctx context.Context, dir string) (v1.PlaygroundDiscovery, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := a.pg.cfg.Discover(ctx, dir)
	if err != nil {
		return v1.PlaygroundDiscovery{}, fmt.Errorf("discover workflows: %w", err)
	}
	var d v1.PlaygroundDiscovery
	if err := json.Unmarshal(raw, &d); err != nil {
		return v1.PlaygroundDiscovery{}, fmt.Errorf("discover workflows: invalid output: %w", err)
	}
	if d.Workflows == nil {
		d.Workflows = []v1.PlaygroundWorkflow{}
	}
	if d.Errors == nil {
		d.Errors = []v1.PlaygroundError{}
	}
	return d, nil
}

// ---------------------------------------------------------------- files ----

func validateFiles(files map[string]string) error {
	if len(files) > maxPlaygroundFiles {
		return fmt.Errorf("%w: at most %d files per project", usecase.ErrInvalidArgument, maxPlaygroundFiles)
	}
	total := 0
	for name, content := range files {
		total += len(content)
		if err := validFileName(name); err != nil {
			return err
		}
	}
	if total > maxPlaygroundBytes {
		return fmt.Errorf("%w: project is larger than %d MiB", usecase.ErrInvalidArgument, maxPlaygroundBytes>>20)
	}
	return nil
}

func validFileName(name string) error {
	if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") {
		return fmt.Errorf("%w: invalid file path %q", usecase.ErrInvalidArgument, name)
	}
	for _, part := range strings.Split(name, "/") {
		if !filePartRe.MatchString(part) || playgroundSkip[part] {
			return fmt.Errorf("%w: invalid file path %q (letters, digits, '_', '-', '.'; no hidden files)", usecase.ErrInvalidArgument, name)
		}
	}
	return nil
}

func readProject(dir string) (map[string]string, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p != dir && validFileName(rel) != nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || validFileName(rel) != nil || strings.HasSuffix(rel, ".pyc") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = string(b)
		return nil
	})
	return files, err
}

// writeProjectFiles makes dir hold exactly files. Unchanged files are left
// alone so their mtime (which drives hot reload on the worker) does not move.
func writeProjectFiles(dir string, files map[string]string) error {
	existing, err := readProject(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name := range existing {
		if _, keep := files[name]; !keep {
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
				return err
			}
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if old, ok := existing[name]; ok && old == files[name] {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(files[name]), 0o644); err != nil {
			return err
		}
	}
	removeEmptyDirs(dir)
	return nil
}

func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != root {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // fails (and is kept) unless empty
	}
}
