// Package cmd implements the `flyke` command line.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/flyke-org/flyke/cli/internal/client"
	"github.com/flyke-org/flyke/cli/internal/local"
)

// Version is set at build time.
var Version = "0.1.0-dev"

type globals struct {
	server    string
	token     string
	namespace string
	debug     bool
	// cluster: use the cluster routes for workers and pools (admin).
	cluster bool
}

var g globals

// exitError carries a process exit code without printing anything more.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

func Execute() int {
	root := &cobra.Command{
		Use:           "flyke",
		Short:         "Code-first durable workflows for application developers",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}
	root.PersistentFlags().StringVar(&g.server, "server", os.Getenv("FLYKE_SERVER"), "control plane URL (env FLYKE_SERVER)")
	root.PersistentFlags().StringVar(&g.token, "token", os.Getenv("FLYKE_TOKEN"), "API token (env FLYKE_TOKEN)")
	root.PersistentFlags().StringVarP(&g.namespace, "namespace", "n", envOr("FLYKE_NAMESPACE", "default"), "namespace")
	root.PersistentFlags().BoolVar(&g.debug, "debug", false, "verbose output")

	root.AddCommand(
		newRunCmd(), newServerCmd(), newDeployCmd(), newRunsCmd(), newLogsCmd(), newEmitCmd(),
		newCancelCmd(), newResumeCmd(), newRetryCmd(), newTaskCmd(), newWorkersCmd(), newPoolsCmd(), newWorkflowsCmd(), newSecretsCmd(),
		newNamespacesCmd(), newTokensCmd(), newWhoAmICmd(), newSchedulesCmd(),
	)
	if err := root.Execute(); err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			return ee.code
		}
		fmt.Fprintln(os.Stderr, newPalette(os.Stderr).red("error:"), err)
		return 1
	}
	return 0
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// resolveServer finds the control plane: --server/FLYKE_SERVER, then a
// running local control plane (`flyke run` / `flyke server --dev`).
func resolveServer(ctx context.Context) (*client.Client, bool) {
	if g.server != "" {
		c := client.New(g.server, g.token, g.namespace)
		c.Cluster = g.cluster
		return c, true
	}
	if st, ok := local.ReadState(); ok {
		c := client.New(st.URL, g.token, g.namespace)
		c.Cluster = g.cluster
		if c.Healthy(ctx) {
			return c, true
		}
	}
	return nil, false
}

func mustClient(ctx context.Context) (*client.Client, error) {
	c, ok := resolveServer(ctx)
	if !ok {
		return nil, errors.New("no control plane found: pass --server (or FLYKE_SERVER), or start one with `flyke server --dev`")
	}
	return c, nil
}

func isLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasPrefix(h, "127.")
}
