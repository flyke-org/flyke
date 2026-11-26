// Command flyke-server runs the Flyke control plane.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flyke-org/flyke/flyke/server"
)

func main() {
	addr := flag.String("addr", envOr("FLYKE_ADDR", ":8742"), "listen address")
	dataDir := flag.String("data-dir", envOr("FLYKE_DATA_DIR", "./flyke-data"), "database and bundle directory")
	adminToken := flag.String("admin-token", os.Getenv("FLYKE_ADMIN_TOKEN"), "require this bearer token on the REST API")
	dev := flag.Bool("dev", false, "relax scheduling for single-machine development")
	debug := flag.Bool("debug", false, "debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(*addr, *dataDir, *adminToken, *dev, log); err != nil {
		fmt.Fprintln(os.Stderr, "flyke-server:", err)
		os.Exit(1)
	}
}

func run(addr, dataDir, adminToken string, dev bool, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv, err := server.New(server.Config{Addr: addr, DataDir: dataDir, AdminToken: adminToken, Dev: dev, Logger: log})
	if err != nil {
		return err
	}
	if err := srv.Start(ctx); err != nil {
		return err
	}
	log.Info("flyke control plane started", "url", srv.URL(), "version", server.Version, "data_dir", dataDir)
	if adminToken == "" {
		log.Warn("REST API is unauthenticated; set --admin-token for shared deployments")
	}
	<-ctx.Done()
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
