// Package server is the composition root of the Flyke control plane. It wires
// adapters to use cases and exposes a small API so the control plane can run
// standalone (cmd/flyke-server) or embedded in-process (the CLI's local mode).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // schedules name IANA time zones; do not depend on the host's zoneinfo

	"github.com/flyke-org/flyke/flyke/internal/adapter/blob"
	"github.com/flyke-org/flyke/flyke/internal/adapter/crypto"
	"github.com/flyke-org/flyke/flyke/internal/adapter/httpapi"
	"github.com/flyke-org/flyke/flyke/internal/adapter/notify"
	"github.com/flyke-org/flyke/flyke/internal/adapter/store/sqlite"
	"github.com/flyke-org/flyke/flyke/internal/adapter/system"
	"github.com/flyke-org/flyke/flyke/internal/adapter/workerapi"
	"github.com/flyke-org/flyke/flyke/internal/app/controller"
	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/app/usecase"
)

// Version is overridden at build time with -ldflags.
var Version = "0.1.0-dev"

type Config struct {
	// Addr to listen on, e.g. "127.0.0.1:8742" or ":8742".
	Addr string
	// DataDir holds the database and bundles. Empty = ephemeral temp dir.
	DataDir string
	// InMemory keeps the database in memory (implies an ephemeral server).
	InMemory bool
	// Dev relaxes scheduling rules for single-machine development.
	Dev bool
	// AdminToken protects the REST API when set.
	AdminToken string
	// ResyncInterval is the controllers' safety-net period (default 1s).
	ResyncInterval time.Duration
	// SecretKey is the base64 AES-256 key that encrypts sensitive secrets.
	// Empty = $FLYKE_SECRET_KEY, else a key file "secret.key" in DataDir
	// (created on first start; back it up with the database).
	SecretKey string
	// UI, when set, is served at "/" as a single-page app (the React dashboard).
	UI fs.FS
	// Discover, when set on a Dev server, enables the UI playground: it runs
	// the SDK's `python -m flyke discover <root>` and returns its output.
	// Projects are stored in DataDir/playground.
	Discover func(ctx context.Context, root string) (json.RawMessage, error)
	Logger   *slog.Logger
}

type Server struct {
	cfg      Config
	svc      *usecase.Service
	store    *sqlite.Store
	http     *http.Server
	listener net.Listener
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	tmpDir   string
}

func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ResyncInterval == 0 {
		cfg.ResyncInterval = time.Second
	}
	s := &Server{cfg: cfg}
	if cfg.DataDir == "" {
		dir, err := os.MkdirTemp("", "flyke-*")
		if err != nil {
			return nil, err
		}
		cfg.DataDir, s.tmpDir = dir, dir
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(cfg.DataDir, "flyke.db")
	if cfg.InMemory {
		dbPath = ":memory:"
	}
	store, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	blobs, err := blob.NewFS(filepath.Join(cfg.DataDir, "bundles"))
	if err != nil {
		return nil, err
	}
	s.store = store
	aead, err := secretCipher(cfg)
	if err != nil {
		store.Close()
		return nil, err
	}
	s.svc = usecase.New(usecase.Deps{
		Store: store, Blobs: blobs, Notifier: notify.NewMemory(),
		Clock: system.Clock{}, IDs: system.IDs{}, Cipher: aead, Logger: cfg.Logger,
	}, usecase.Config{Dev: cfg.Dev})

	mux := http.NewServeMux()
	path, workerHandler := workerapi.New(s.svc)
	mux.Handle(path, workerHandler)
	var pg *httpapi.Playground
	if cfg.Dev && cfg.Discover != nil {
		pg = &httpapi.Playground{Dir: filepath.Join(cfg.DataDir, "playground"), Discover: cfg.Discover}
	}
	mux.Handle("/api/", httpapi.New(s.svc, cfg.AdminToken, cfg.Logger, pg))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, Version) })
	if cfg.UI != nil {
		// Method-less "/" so the more specific /api/ and protocol paths win.
		mux.Handle("/", spa(cfg.UI))
	}

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true) // gRPC clients over h2c
	s.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		Protocols:         protocols,
	}
	s.cfg = cfg
	return s, nil
}

// Start listens and runs controllers in the background.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.listener = ln
	ctx, s.cancel = context.WithCancel(ctx)
	// Databases written before schedules were resources have cron triggers
	// in their manifests only.
	if err := s.svc.AdoptDeclaredSchedules(ctx); err != nil {
		s.cfg.Logger.Warn("could not adopt schedules declared in workflows", "err", err)
	}

	resync := s.cfg.ResyncInterval
	mgr := controller.NewManager(s.svc.Notifier(), s.cfg.Logger,
		controller.Reconciler{Name: "admission", Reconcile: s.svc.ReconcileAdmission, Resync: resync, Wake: true},
		controller.Reconciler{Name: "steps", Reconcile: s.svc.ReconcileSteps, Resync: resync, Wake: true},
		controller.Reconciler{Name: "compensation", Reconcile: s.svc.ReconcileCompensation, Resync: resync, Wake: true},
		controller.Reconciler{Name: "leases", Reconcile: s.svc.ReconcileLeases, Resync: resync, Wake: true},
		controller.Reconciler{Name: "waits", Reconcile: s.svc.ReconcileWaits, Resync: resync / 2},
		controller.Reconciler{Name: "deadlines", Reconcile: s.svc.ReconcileRunDeadlines, Resync: 2 * resync},
		controller.Reconciler{Name: "workers", Reconcile: s.svc.ReconcileWorkers, Resync: 5 * resync},
		// Timer-driven: sleeps until the next tick is due instead of polling.
		controller.Reconciler{Name: "schedules", Plan: s.svc.ReconcileSchedules, Topic: ports.TopicSchedules, Resync: time.Minute},
	)
	s.wg.Add(2)
	go func() { defer s.wg.Done(); mgr.Run(ctx) }()
	go func() {
		defer s.wg.Done()
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.cfg.Logger.Error("http server stopped", "err", err)
		}
	}()
	s.cfg.Logger.Debug("control plane listening", "addr", ln.Addr().String(), "data_dir", s.cfg.DataDir)
	return nil
}

// spa serves static files, falling back to index.html for client-side routes.
func spa(files fs.FS) http.Handler {
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" {
			if st, err := fs.Stat(files, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, files, "index.html")
	})
}

// HasUI reports whether the dashboard is being served.
func (s *Server) HasUI() bool { return s.cfg.UI != nil }

// URL is the base URL clients and workers use.
func (s *Server) URL() string {
	addr := s.listener.Addr().(*net.TCPAddr)
	host := addr.IP.String()
	if addr.IP.IsUnspecified() {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, fmt.Sprint(addr.Port)))
}

// CreateJoinToken issues a join token (used by local mode to start its worker).
func (s *Server) CreateJoinToken(ctx context.Context, pool string, ttl time.Duration) (string, error) {
	_, plain, err := s.svc.CreateJoinToken(ctx, "", pool, ttl, 0, nil)
	return plain, err
}

// Shutdown stops serving and waits for background loops.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	err := s.http.Shutdown(ctx)
	if err != nil {
		_ = s.http.Close()
	}
	s.wg.Wait()
	_ = s.store.Close()
	if s.tmpDir != "" {
		_ = os.RemoveAll(s.tmpDir)
	}
	return err
}

// secretCipher builds the cipher of sensitive secrets from cfg.SecretKey,
// $FLYKE_SECRET_KEY or the data directory's key file, in that order.
func secretCipher(cfg Config) (*crypto.AEAD, error) {
	raw := cfg.SecretKey
	if raw == "" {
		raw = os.Getenv("FLYKE_SECRET_KEY")
	}
	var key []byte
	var err error
	if raw != "" {
		key, err = crypto.ParseKey(raw)
	} else {
		key, err = crypto.LoadOrCreateKeyFile(filepath.Join(cfg.DataDir, "secret.key"))
	}
	if err != nil {
		return nil, fmt.Errorf("secret key: %w", err)
	}
	return crypto.New(key)
}
