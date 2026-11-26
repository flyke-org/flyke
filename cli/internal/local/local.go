// Package local runs the "batteries included" development mode: an in-process
// control plane plus a worker subprocess, connected by the regular protocol.
package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/flyke-org/flyke/flyke/server"
)

// DefaultAddr is where local control planes listen when the port is free, so
// that other CLI invocations (emit, logs, resume…) find them.
const DefaultAddr = "127.0.0.1:8742"

// StateFile records a running local control plane.
type StateFile struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

func statePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".flyke", "local.json")
}

func ReadState() (StateFile, bool) {
	var s StateFile
	b, err := os.ReadFile(statePath())
	if err != nil || json.Unmarshal(b, &s) != nil || s.URL == "" {
		return s, false
	}
	return s, true
}

func writeState(s StateFile) error {
	p := statePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	return os.WriteFile(p, b, 0o600)
}

func clearState(pid int) {
	if s, ok := ReadState(); ok && s.PID == pid {
		_ = os.Remove(statePath())
	}
}

// FindPython locates an interpreter with the flyke SDK and worker installed.
func FindPython(hints ...string) (string, error) {
	var candidates []string
	if p := os.Getenv("FLYKE_PYTHON"); p != "" {
		candidates = append(candidates, p)
	}
	if venv := os.Getenv("VIRTUAL_ENV"); venv != "" {
		candidates = append(candidates, filepath.Join(venv, "bin", "python"))
	}
	cwd, _ := os.Getwd()
	for _, start := range append(hints, cwd) {
		dir, _ := filepath.Abs(start)
		for {
			candidates = append(candidates, filepath.Join(dir, ".venv", "bin", "python"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	candidates = append(candidates, "python3", "python")
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[c] {
			continue
		}
		seen[c] = true
		path, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		if exec.Command(path, "-c", "import flyke, flyke_worker").Run() == nil {
			return path, nil
		}
	}
	return "", errors.New("no Python interpreter with `flyke` and `flyke-worker` installed was found; " +
		"run `uv sync` in the repository (or `pip install flyke flyke-worker`), or set FLYKE_PYTHON")
}

// Manifest asks the SDK to describe a workflow without running it.
type Manifest struct {
	Root     string          `json:"root"`
	Manifest json.RawMessage `json:"manifest"`
}

func ExtractManifest(ctx context.Context, python, target, root string) (Manifest, error) {
	args := []string{"-m", "flyke", "manifest", target}
	if root != "" {
		args = append(args, "--root", root)
	}
	cmd := exec.CommandContext(ctx, python, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(bytes.TrimSpace(lastLine(stderr.Bytes())), &e) == nil && e.Error != "" {
			return Manifest{}, fmt.Errorf("cannot load %s: %s", target, e.Error)
		}
		return Manifest{}, fmt.Errorf("cannot load %s: %v\n%s", target, err, stderr.String())
	}
	var m Manifest
	if err := json.Unmarshal(stdout.Bytes(), &m); err != nil {
		return Manifest{}, fmt.Errorf("invalid manifest output: %w", err)
	}
	return m, nil
}

// Discover lists every workflow under root (`python -m flyke discover`).
// Import errors are part of the output, not an error.
func Discover(ctx context.Context, python, root string) (json.RawMessage, error) {
	cmd := exec.CommandContext(ctx, python, "-m", "flyke", "discover", root)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	out := bytes.TrimSpace(lastLine(stdout.Bytes()))
	if !json.Valid(out) {
		return nil, fmt.Errorf("unexpected output from python -m flyke discover: %s", stdout.String())
	}
	return out, nil
}

func lastLine(b []byte) []byte {
	b = bytes.TrimSpace(b)
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		return b[i+1:]
	}
	return b
}

// Cluster is an embedded control plane with one local worker.
type Cluster struct {
	Server *server.Server
	worker *exec.Cmd
	logf   *os.File
	pid    int
}

type Options struct {
	Python string
	// WithWorker starts a local worker subprocess.
	WithWorker bool
	Slots      int
	// Addr overrides DefaultAddr.
	Addr string
	// DataDir persists state; empty means ephemeral (in-memory).
	DataDir string
	Logger  *slog.Logger
	// WorkerOutput receives the worker's own logs (default: a temp file).
	WorkerOutput io.Writer
	// UI is the dashboard to serve (nil = API only).
	UI fs.FS
}

// Start boots the control plane (and worker) and returns once both are ready.
func Start(ctx context.Context, o Options) (*Cluster, error) {
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	addr := o.Addr
	if addr == "" {
		addr = DefaultAddr
		if ln, err := net.Listen("tcp", addr); err != nil {
			addr = "127.0.0.1:0" // another local server runs; take any port
		} else {
			ln.Close()
		}
	}
	cfg := server.Config{
		Addr: addr, DataDir: o.DataDir, InMemory: o.DataDir == "", Dev: true,
		ResyncInterval: 250 * time.Millisecond, Logger: o.Logger, UI: o.UI,
	}
	if o.Python != "" {
		python := o.Python
		cfg.Discover = func(ctx context.Context, root string) (json.RawMessage, error) {
			return Discover(ctx, python, root)
		}
	}
	srv, err := server.New(cfg)
	if err != nil {
		return nil, err
	}
	if err := srv.Start(ctx); err != nil {
		return nil, err
	}
	c := &Cluster{Server: srv, pid: os.Getpid()}
	_ = writeState(StateFile{URL: srv.URL(), PID: c.pid})
	if !o.WithWorker {
		return c, nil
	}
	token, err := srv.CreateJoinToken(ctx, "local", 10*time.Minute)
	if err != nil {
		c.Stop()
		return nil, err
	}
	stateDir, err := os.MkdirTemp("", "flyke-worker-*")
	if err != nil {
		c.Stop()
		return nil, err
	}
	slots := o.Slots
	if slots <= 0 {
		slots = 16
	}
	cmd := exec.Command(o.Python, "-m", "flyke_worker", "--state-dir", stateDir, "join",
		"--url", srv.URL(), "--token", token, "--name", fmt.Sprintf("local-%d", c.pid),
		"--ephemeral", "--slots", fmt.Sprint(slots))
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	if o.WorkerOutput != nil {
		cmd.Stdout, cmd.Stderr = o.WorkerOutput, o.WorkerOutput
	} else {
		f, err := os.CreateTemp("", "flyke-local-worker-*.log")
		if err == nil {
			c.logf = f
			cmd.Stdout, cmd.Stderr = f, f
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		c.Stop()
		return nil, fmt.Errorf("start local worker: %w", err)
	}
	c.worker = cmd
	return c, nil
}

// WorkerLog returns the path of the local worker's log file, if any.
func (c *Cluster) WorkerLog() string {
	if c.logf == nil {
		return ""
	}
	return c.logf.Name()
}

// WorkerExited reports whether the worker process died.
func (c *Cluster) WorkerExited() bool {
	if c.worker == nil || c.worker.Process == nil {
		return false
	}
	return c.worker.ProcessState != nil || c.worker.Process.Signal(syscall.Signal(0)) != nil
}

// Stop drains the worker and shuts the control plane down.
func (c *Cluster) Stop() {
	if c.worker != nil && c.worker.Process != nil {
		_ = c.worker.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = c.worker.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-c.worker.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Server.Shutdown(ctx)
	clearState(c.pid)
	if c.logf != nil {
		c.logf.Close()
		if !strings.Contains(os.Getenv("FLYKE_KEEP_LOGS"), "1") {
			_ = os.Remove(c.logf.Name())
		}
	}
}
