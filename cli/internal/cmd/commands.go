package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
	"github.com/flyke-org/flyke/cli/internal/bundle"
	"github.com/flyke-org/flyke/cli/internal/client"
	"github.com/flyke-org/flyke/cli/internal/local"
	"github.com/flyke-org/flyke/flyke/server"
	"github.com/flyke-org/flyke/ui"
)

// ---------------------------------------------------------------- server ----

func newServerCmd() *cobra.Command {
	var addr, dataDir, adminToken string
	var dev, withWorker bool
	var slots int
	c := &cobra.Command{
		Use:   "server",
		Short: "Run the control plane (--dev adds a local worker: a complete dev environment)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			level := slog.LevelInfo
			if g.debug {
				level = slog.LevelDebug
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
			if dev {
				python, err := local.FindPython()
				if err != nil && withWorker {
					return err
				}
				if addr == "" {
					addr = local.DefaultAddr
				}
				cl, err := local.Start(context.WithoutCancel(ctx), local.Options{
					Python: python, WithWorker: withWorker, Slots: slots, Addr: addr, DataDir: dataDir,
					Logger: logger, WorkerOutput: os.Stderr, UI: ui.FS(),
				})
				if err != nil {
					return err
				}
				defer cl.Stop()
				logger.Info("flyke dev control plane started", "url", cl.Server.URL(), "local_worker", withWorker, "persistent", dataDir != "")
				announceUI(cl.Server)
				<-ctx.Done()
				return nil
			}
			if addr == "" {
				addr = ":8742"
			}
			if dataDir == "" {
				dataDir = "./flyke-data"
			}
			srv, err := server.New(server.Config{Addr: addr, DataDir: dataDir, AdminToken: adminToken, Logger: logger, UI: ui.FS()})
			if err != nil {
				return err
			}
			if err := srv.Start(ctx); err != nil {
				return err
			}
			logger.Info("flyke control plane started", "url", srv.URL(), "data_dir", dataDir)
			announceUI(srv)
			if adminToken == "" {
				logger.Warn("REST API is unauthenticated; set --admin-token (or FLYKE_ADMIN_TOKEN) for shared deployments")
			}
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return srv.Shutdown(sctx)
		},
	}
	c.Flags().StringVar(&addr, "addr", "", "listen address (default :8742, dev: 127.0.0.1:8742)")
	c.Flags().StringVar(&dataDir, "data-dir", "", "persist state here (dev default: in-memory)")
	c.Flags().StringVar(&adminToken, "admin-token", os.Getenv("FLYKE_ADMIN_TOKEN"), "require this bearer token on the REST API")
	c.Flags().BoolVar(&dev, "dev", false, "development mode: relaxed scheduling, local discovery")
	c.Flags().BoolVar(&withWorker, "worker", true, "with --dev, also start a local worker")
	c.Flags().IntVar(&slots, "slots", 16, "local worker slots")
	return c
}

func announceUI(srv *server.Server) {
	if srv.HasUI() {
		fmt.Fprintf(os.Stderr, "dashboard: %s\n", srv.URL())
	} else {
		fmt.Fprintln(os.Stderr, "dashboard not built into this binary (run `make ui build`)")
	}
}

// ---------------------------------------------------------------- deploy ----

func newDeployCmd() *cobra.Command {
	var root string
	c := &cobra.Command{
		Use:   "deploy <file.py:Workflow>...",
		Short: "Upload code and register workflows (enables their triggers)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := mustClient(ctx)
			if err != nil {
				return err
			}
			for _, target := range args {
				file, _, _ := strings.Cut(target, ":")
				python, err := local.FindPython(file)
				if err != nil {
					return err
				}
				m, err := local.ExtractManifest(ctx, python, target, root)
				if err != nil {
					return err
				}
				var meta struct {
					Name       string `json:"name"`
					Entrypoint string `json:"entrypoint"`
				}
				_ = json.Unmarshal(m.Manifest, &meta)
				data, err := bundle.Pack(m.Root)
				if err != nil {
					return err
				}
				up, err := c.UploadBundle(ctx, bytes.NewReader(data))
				if err != nil {
					return err
				}
				wf, err := c.PutWorkflow(ctx, meta.Name, v1.WorkflowSpec{Manifest: m.Manifest, Source: v1.Source{Entrypoint: meta.Entrypoint, BundleDigest: up.Digest}})
				if err != nil {
					return err
				}
				fmt.Printf("workflow/%s %s (bundle %s, %d bytes)\n", wf.Metadata.Name, wf.Status.Version, up.Digest[:19], up.Size)
				// Say what the deploy scheduled, so on.schedule(...) is never a silent effect.
				if scs, err := c.ListSchedules(ctx, wf.Metadata.Name); err == nil {
					for _, sc := range scs {
						next := "paused"
						if sc.Status.NextRunAt != nil {
							next = "next run " + until(*sc.Status.NextRunAt)
						}
						fmt.Printf("  schedule/%s: %s, %s\n", sc.Metadata.Name, sc.Status.Description, next)
					}
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&root, "root", "", "code root to bundle (default: the file's directory)")
	return c
}

// ------------------------------------------------------------------ runs ----

func newRunsCmd() *cobra.Command {
	c := &cobra.Command{Use: "runs", Short: "Inspect workflow runs"}
	var workflow, version, phase string
	var limit int
	ls := &cobra.Command{
		Use: "ls", Short: "List runs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			if version != "" && workflow != "" {
				if version, err = resolveVersion(cmd.Context(), c, workflow, version); err != nil {
					return err
				}
			}
			runs, err := c.ListRuns(cmd.Context(), workflow, version, phase, limit)
			if err != nil {
				return err
			}
			p := newPalette(os.Stdout)
			rows := [][]string{}
			for _, r := range runs {
				rows = append(rows, []string{r.Metadata.Name, r.Spec.Workflow, r.Spec.Version, p.phase(r.Status.Phase), r.Status.Reason, age(r.Metadata.CreatedAt)})
			}
			table(os.Stdout, []string{"RUN", "WORKFLOW", "VERSION", "PHASE", "REASON", "AGE"}, rows)
			return nil
		},
	}
	ls.Flags().StringVar(&workflow, "workflow", "", "filter by workflow")
	ls.Flags().StringVar(&version, "version", "", "filter by workflow version (a prefix works with --workflow)")
	ls.Flags().StringVar(&phase, "phase", "", "filter by phase (comma-separated)")
	ls.Flags().IntVar(&limit, "limit", 50, "max runs")

	var asJSON, history bool
	get := &cobra.Command{
		Use: "get <run>", Short: "Show a run and its tasks", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := mustClient(ctx)
			if err != nil {
				return err
			}
			r, err := c.GetRun(ctx, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				printJSON(os.Stdout, r)
				return nil
			}
			p := newPalette(os.Stdout)
			fmt.Printf("%s  %s %s  %s\n", p.bold(r.Metadata.Name), r.Spec.Workflow, p.dim(r.Spec.Version), p.phase(r.Status.Phase))
			if r.Status.Message != "" {
				fmt.Printf("  %s: %s\n", r.Status.Reason, r.Status.Message)
			}
			fmt.Printf("  inputs: %s\n", string(r.Spec.Inputs))
			if len(r.Status.Result) > 0 {
				fmt.Printf("  result: %s\n", string(r.Status.Result))
			}
			tasks, err := c.RunTasks(ctx, args[0])
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, t := range tasks {
				name := t.Name
				if t.Kind == "compensation" {
					name += " (undo)"
				}
				rows = append(rows, []string{strconv.Itoa(t.Ref), name, p.phase(phaseAlias(t.Phase)), strconv.Itoa(t.Attempt), t.Worker, durationOf(t)})
			}
			fmt.Println()
			table(os.Stdout, []string{"REF", "TASK", "PHASE", "ATTEMPT", "WORKER", "DURATION"}, rows)
			if history {
				evs, err := c.History(ctx, args[0])
				if err != nil {
					return err
				}
				fmt.Println()
				hrows := [][]string{}
				for _, ev := range evs {
					hrows = append(hrows, []string{strconv.Itoa(ev.ID), ev.Time.Local().Format("15:04:05.000"), ev.Type, strconv.Itoa(ev.Ref), ev.Name})
				}
				table(os.Stdout, []string{"#", "TIME", "EVENT", "REF", "NAME"}, hrows)
			}
			return nil
		},
	}
	get.Flags().BoolVar(&asJSON, "json", false, "print the raw resource")
	get.Flags().BoolVar(&history, "history", false, "also print the event history")
	c.AddCommand(ls, get)
	return c
}

func phaseAlias(p string) string {
	if p == "Unknown" {
		return "Paused"
	}
	return p
}

func durationOf(t v1.TaskRun) string {
	if t.StartedAt == nil {
		return "-"
	}
	end := time.Now()
	if t.FinishedAt != nil {
		end = *t.FinishedAt
	}
	return end.Sub(*t.StartedAt).Round(time.Millisecond).String()
}

func newLogsCmd() *cobra.Command {
	var follow bool
	c := &cobra.Command{
		Use: "logs <run>", Short: "Print a run's logs (-f to follow until it ends)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			c, err := mustClient(ctx)
			if err != nil {
				return err
			}
			p := newPalette(os.Stdout)
			if follow {
				return c.FollowLogs(ctx, args[0], 0, func(ev client.LogEvent) error {
					if ev.Log != nil {
						printLog(os.Stdout, p, *ev.Log, g.debug)
					} else if ev.End != nil {
						fmt.Printf("\n%s %s\n", ev.End.Metadata.Name, p.phase(ev.End.Status.Phase))
					}
					return nil
				})
			}
			entries, err := c.ListLogs(ctx, args[0])
			if err != nil {
				return err
			}
			for _, e := range entries {
				printLog(os.Stdout, p, e, g.debug)
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "follow until the run ends")
	return c
}

func newEmitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "emit <event> [key=value ...]",
		Short: "Publish an event (resumes waiting runs, fires event triggers)",
		Example: `  flyke emit deploy.approved service=api user=alice
  flyke emit git.pushed branch=main service=billing`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			payload := map[string]any{}
			for _, kv := range args[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("invalid field %q; expected key=value", kv)
				}
				var parsed any
				if json.Unmarshal([]byte(v), &parsed) == nil {
					if _, isStr := parsed.(string); !isStr {
						payload[k] = parsed
						continue
					}
				}
				payload[k] = v
			}
			raw, _ := json.Marshal(payload)
			ev, err := c.Emit(cmd.Context(), args[0], raw)
			if err != nil {
				return err
			}
			if len(ev.Matched) == 0 {
				fmt.Printf("event %s published (no run matched)\n", ev.Name)
			} else {
				fmt.Printf("event %s published → %s\n", ev.Name, strings.Join(ev.Matched, ", "))
			}
			return nil
		},
	}
}

func newCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use: "cancel <run>", Short: "Cancel a run (completed tasks with compensations are undone)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			r, err := c.CancelRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("%s %s\n", r.Metadata.Name, r.Status.Phase)
			return nil
		},
	}
}

func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use: "resume <run>", Short: "Resume a paused run", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			r, err := c.ResumeRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("%s %s\n", r.Metadata.Name, r.Status.Phase)
			return nil
		},
	}
}

func newRetryCmd() *cobra.Command {
	var from string
	var all, follow bool
	var set []string
	var version string
	c := &cobra.Command{
		Use:   "retry <run> [--from <task|ref>] [--all] [--set key=value]... [--version same|<v>] [-f]",
		Short: "Start a new run from a finished one, reusing what already succeeded",
		Long: `Start a new run of the latest code from a finished run.

By default only what failed (or never ran) executes again: every task that
succeeded is reused with its recorded result.

  flyke retry <run>                        re-run only failed / not-reached steps
  flyke retry <run> --from embed_document  reuse everything before embed_document,
                                           run it and everything after again
  flyke retry <run> --from 4               same, by ref (see the run's Tasks tab)
  flyke retry <run> --all                  run everything again, same inputs
  flyke retry <run> --set chunk_size=4     override inputs (JSON values allowed)
  flyke retry <run> --version same         retry with the code the run used,
                                           not the current version

A recorded result is reused only if the task is called at the same position
with the same arguments, so after a code or input change whatever changed runs.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := mustClient(ctx)
			if err != nil {
				return err
			}
			req := v1.RetryRunRequest{Mode: "failed", Version: version}
			switch {
			case all && from != "":
				return errors.New("--all and --from are mutually exclusive")
			case all:
				req.Mode = "all"
			case from != "":
				req.Mode = "from"
				if n, err := strconv.Atoi(from); err == nil {
					req.FromRef = n
				} else {
					req.FromTask = from
				}
			}
			if len(set) > 0 {
				src, err := c.GetRun(ctx, args[0])
				if err != nil {
					return err
				}
				inputs := map[string]any{}
				_ = json.Unmarshal(src.Spec.Inputs, &inputs)
				for _, kv := range set {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						return fmt.Errorf("--set %q: want key=value", kv)
					}
					var val any
					if json.Unmarshal([]byte(v), &val) != nil {
						val = v
					}
					inputs[k] = val
				}
				req.Inputs, _ = json.Marshal(inputs)
			}
			r, err := c.RetryRun(ctx, args[0], req)
			if err != nil {
				return err
			}
			p := newPalette(os.Stderr)
			what := map[string]string{"all": "everything runs again", "failed": "reusing every successful task"}[req.Mode]
			if req.Mode == "from" && r.Spec.Retry != nil {
				what = fmt.Sprintf("reusing everything before %s (ref %d)", r.Spec.Retry.FromTask, r.Spec.Retry.FromRef)
			}
			fmt.Fprintf(os.Stderr, "%s %s %s\n", p.green("↻"), r.Metadata.Name, p.dim("retry of "+args[0]+": "+what))
			fmt.Fprintln(os.Stderr, p.dim("  "+strings.TrimRight(c.BaseURL, "/")+"/runs/"+r.Metadata.Name))
			if !follow {
				fmt.Println(r.Metadata.Name)
				return nil
			}
			return followRun(ctx, c, r.Metadata.Name, true, nil)
		},
	}
	c.Flags().StringVar(&from, "from", "", "restart from this task name (first call) or ref")
	c.Flags().BoolVar(&all, "all", false, "run everything again (reuse nothing)")
	c.Flags().StringVar(&version, "version", "", `code to retry with: "same" (the run's version) or a version; default: current`)
	c.Flags().StringArrayVar(&set, "set", nil, "override an input: key=value (repeatable)")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "stream logs until the new run ends")
	return c
}

func newTaskCmd() *cobra.Command {
	c := &cobra.Command{Use: "task", Short: "Operate on tasks"}
	var outcome, result, message string
	resolve := &cobra.Command{
		Use:   "resolve <run> <ref> --outcome=succeeded|failed|retry",
		Short: "Settle a task whose outcome is unknown (AT_MOST_ONCE + on_unknown=pause)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("ref must be a number: %w", err)
			}
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			req := v1.ResolveTaskRequest{Outcome: outcome, Message: message}
			if result != "" {
				if !json.Valid([]byte(result)) {
					return fmt.Errorf("--result must be JSON")
				}
				req.Result = json.RawMessage(result)
			}
			r, err := c.ResolveTask(cmd.Context(), args[0], ref, req)
			if err != nil {
				return err
			}
			fmt.Printf("%s %s\n", r.Metadata.Name, r.Status.Phase)
			return nil
		},
	}
	resolve.Flags().StringVar(&outcome, "outcome", "", "succeeded | failed | retry")
	resolve.Flags().StringVar(&result, "result", "", "JSON result when --outcome=succeeded")
	resolve.Flags().StringVar(&message, "message", "", "reason when --outcome=failed")
	_ = resolve.MarkFlagRequired("outcome")
	c.AddCommand(resolve)
	return c
}

// --------------------------------------------------------------- workers ----

func newWorkersCmd() *cobra.Command {
	c := &cobra.Command{Use: "workers", Aliases: []string{"worker"}, Short: "Manage the namespace's workers (-A: every worker, admin)"}
	c.PersistentFlags().BoolVarP(&g.cluster, "all-namespaces", "A", false, "act on the workers of every namespace and on shared workers (admin)")
	var selector string
	ls := &cobra.Command{
		Use: "ls", Short: "List registered workers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			ws, err := c.ListWorkers(cmd.Context(), selector)
			if err != nil {
				return err
			}
			p := newPalette(os.Stdout)
			rows := [][]string{}
			for _, w := range ws {
				status := w.Status.Phase
				if w.Spec.Draining {
					status += ",Draining"
				} else if w.Spec.Unschedulable {
					status += ",SchedulingDisabled"
				}
				if w.Status.Phase == "Ready" {
					status = p.green(status)
				} else {
					status = p.red(status)
				}
				hb := "-"
				if w.Status.LastHeartbeat != nil {
					hb = age(*w.Status.LastHeartbeat)
				}
				rows = append(rows, []string{w.Metadata.Name, orShared(w.Metadata.Namespace), status, w.Status.Pool,
					fmt.Sprintf("%d/%d", w.Status.Slots-w.Status.FreeSlots, w.Status.Slots),
					strings.Join(w.Status.Runtimes, ","), hb, labelString(w.Metadata.Labels)})
			}
			table(os.Stdout, []string{"NAME", "NAMESPACE", "STATUS", "POOL", "BUSY", "RUNTIMES", "HEARTBEAT", "LABELS"}, rows)
			return nil
		},
	}
	ls.Flags().StringVarP(&selector, "selector", "l", "", "label selector (k=v,k2=v2)")
	action := func(use, short, verb string) *cobra.Command {
		return &cobra.Command{
			Use: use + " <worker>", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := mustClient(cmd.Context())
				if err != nil {
					return err
				}
				if verb == "rm" {
					if err := c.DeleteWorker(cmd.Context(), args[0]); err != nil {
						return err
					}
					fmt.Printf("worker/%s removed (credential revoked)\n", args[0])
					return nil
				}
				if _, err := c.WorkerAction(cmd.Context(), args[0], verb); err != nil {
					return err
				}
				fmt.Printf("worker/%s %sed\n", args[0], strings.TrimSuffix(verb, "e"))
				return nil
			},
		}
	}
	c.AddCommand(ls,
		action("rm", "Remove a worker and revoke its credential", "rm"),
		action("cordon", "Stop scheduling new work on a worker", "cordon"),
		action("uncordon", "Allow scheduling on a worker again", "uncordon"),
		action("drain", "Finish running work, then stop the worker", "drain"),
	)
	return c
}

func labelString(l map[string]string) string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + l[k]
	}
	return strings.Join(parts, ",")
}

// ----------------------------------------------------------------- pools ----

func newPoolsCmd() *cobra.Command {
	c := &cobra.Command{Use: "pools", Aliases: []string{"pool"}, Short: "Manage the namespace's worker pools and join tokens (--shared: shared pools, admin)"}
	c.PersistentFlags().BoolVar(&g.cluster, "shared", false, "act on shared pools, which serve every namespace allowed to use them, and list all pools (admin)")
	ls := &cobra.Command{
		Use: "ls", Short: "List pools",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			ps, err := c.ListPools(cmd.Context())
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, pl := range ps {
				profiles := make([]string, 0)
				for k := range pl.Spec.ResourceProfiles {
					profiles = append(profiles, k)
				}
				sort.Strings(profiles)
				rows = append(rows, []string{pl.Metadata.Name, orShared(pl.Metadata.Namespace), labelString(pl.Spec.Selector),
					fmt.Sprintf("%d/%d", pl.Status.ReadyWorkers, pl.Status.Workers), strconv.Itoa(pl.Status.FreeSlots), strings.Join(profiles, ",")})
			}
			table(os.Stdout, []string{"POOL", "NAMESPACE", "SELECTOR", "READY", "FREE SLOTS", "PROFILES"}, rows)
			return nil
		},
	}
	var selector []string
	var maxConcurrent int
	var profiles []string
	create := &cobra.Command{
		Use: "create <pool>", Short: "Create or update a pool", Args: cobra.ExactArgs(1),
		Example: `  flyke pools create gpu --selector gpu=a10 --profile heavy=cpu:8,memory:32Gi`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			spec := v1.PoolSpec{MaxConcurrentTasks: maxConcurrent}
			if len(selector) > 0 {
				spec.Selector = map[string]string{}
				for _, kv := range selector {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						return fmt.Errorf("invalid selector %q", kv)
					}
					spec.Selector[k] = v
				}
			}
			for _, prof := range profiles {
				name, res, err := parseProfile(prof)
				if err != nil {
					return err
				}
				if spec.ResourceProfiles == nil {
					spec.ResourceProfiles = map[string]v1.Resources{}
				}
				spec.ResourceProfiles[name] = res
			}
			pl, err := c.PutPool(cmd.Context(), args[0], spec)
			if err != nil {
				return err
			}
			fmt.Printf("pool/%s configured (selector %s)\n", pl.Metadata.Name, labelString(pl.Spec.Selector))
			return nil
		},
	}
	create.Flags().StringSliceVar(&selector, "selector", nil, "worker label selector k=v (default flyke.io/pool=<pool>)")
	create.Flags().IntVar(&maxConcurrent, "max-concurrent", 0, "max running tasks in the pool (0 = unlimited)")
	create.Flags().StringArrayVar(&profiles, "profile", nil, "resource profile name=cpu:N,memory:SIZE")

	rm := &cobra.Command{
		Use: "rm <pool>", Short: "Delete a pool and its join tokens", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			return c.DeletePool(cmd.Context(), args[0])
		},
	}

	var ttl time.Duration
	var maxUses int
	var allowed []string
	token := &cobra.Command{
		Use:   "token <pool>",
		Short: "Create a join token and print the command that registers a host",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			req := v1.CreateJoinTokenRequest{TTLSeconds: int(ttl.Seconds()), MaxUses: maxUses}
			if len(allowed) > 0 {
				req.AllowedLabels = map[string]string{}
				for _, kv := range allowed {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						v = "*"
					}
					req.AllowedLabels[k] = v
				}
			}
			tok, err := c.CreateJoinToken(cmd.Context(), args[0], req)
			if err != nil {
				return err
			}
			p := newPalette(os.Stdout)
			fmt.Printf("join token for pool %s (expires %s):\n\n  %s\n\n", tok.Pool, tok.ExpiresAt.Local().Format(time.RFC3339), p.bold(tok.Token))
			fmt.Println("register a host with:")
			fmt.Printf("\n  flyke-worker join --url %s --token %s\n\n", c.BaseURL, tok.Token)
			fmt.Println("or with Docker:")
			fmt.Printf("\n  docker run -d --restart=always -v flyke-worker:/var/lib/flyke-worker \\\n    ghcr.io/flyke-org/worker-python join --url %s --token %s\n", c.BaseURL, tok.Token)
			return nil
		},
	}
	token.Flags().DurationVar(&ttl, "ttl", time.Hour, "token lifetime")
	token.Flags().IntVar(&maxUses, "max-uses", 0, "max registrations (0 = unlimited)")
	token.Flags().StringSliceVar(&allowed, "allow-label", nil, "labels workers may set (k=v, or k for any value); default: any")
	c.AddCommand(ls, create, rm, token)
	return c
}

func parseProfile(s string) (string, v1.Resources, error) {
	name, spec, ok := strings.Cut(s, "=")
	if !ok || name == "" {
		return "", v1.Resources{}, fmt.Errorf("invalid profile %q; expected name=cpu:N,memory:SIZE", s)
	}
	var res v1.Resources
	for _, part := range strings.Split(spec, ",") {
		k, v, _ := strings.Cut(part, ":")
		switch k {
		case "cpu":
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return "", res, fmt.Errorf("profile %s: cpu: %w", name, err)
			}
			res.CPU = f
		case "memory":
			b, err := parseSize(v)
			if err != nil {
				return "", res, fmt.Errorf("profile %s: memory: %w", name, err)
			}
			res.MemoryBytes = b
		default:
			return "", res, fmt.Errorf("profile %s: unknown resource %q", name, k)
		}
	}
	return name, res, nil
}

func parseSize(s string) (int64, error) {
	units := []struct {
		suffix string
		mult   int64
	}{{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"K", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			f, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			return int64(f * float64(u.mult)), err
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

// ------------------------------------------------------------- workflows ----

func newWorkflowsCmd() *cobra.Command {
	c := &cobra.Command{Use: "workflows", Aliases: []string{"wf"}, Short: "List workflows and manage their versions (versions, activate, rollback)"}
	c.AddCommand(&cobra.Command{
		Use: "ls", Short: "List workflows",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			wfs, err := c.ListWorkflows(cmd.Context())
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, wf := range wfs {
				var m struct {
					Triggers []struct {
						Type, Event, Cron string
					} `json:"triggers"`
				}
				_ = json.Unmarshal(wf.Spec.Manifest, &m)
				var trig []string
				for _, t := range m.Triggers {
					trig = append(trig, t.Type+":"+t.Event+t.Cron)
				}
				src := "local"
				if wf.Spec.Source.BundleDigest != "" {
					src = wf.Spec.Source.BundleDigest[:19]
				}
				activated := wf.Metadata.CreatedAt
				if wf.Status.ActivatedAt != nil {
					activated = *wf.Status.ActivatedAt
				}
				rows = append(rows, []string{wf.Metadata.Name, wf.Status.Version, src, strings.Join(trig, ","), age(activated)})
			}
			table(os.Stdout, []string{"WORKFLOW", "VERSION", "SOURCE", "TRIGGERS", "ACTIVE FOR"}, rows)
			return nil
		},
	})
	c.AddCommand(newVersionsCmd(), newActivateCmd(), newRollbackCmd())
	return c
}

// ------------------------------------------------------------- versions ----

func newVersionsCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:     "versions <workflow> [version]",
		Aliases: []string{"history"},
		Short:   "List a workflow's versions with their code and runs (or show one)",
		Long: `List every registered version of a workflow, current first, then in
order of their last activation. Each row shows when the version was
registered and last made current, its code bundle, and the runs that used it.

  flyke workflows versions Hello                 all versions
  flyke workflows versions Hello 5cde48          one version (a prefix works)
  flyke runs ls --workflow Hello --version 5cde48  the runs of that version`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := mustClient(ctx)
			if err != nil {
				return err
			}
			if len(args) == 2 {
				version, err := resolveVersion(ctx, c, args[0], args[1])
				if err != nil {
					return err
				}
				v, err := c.GetWorkflowVersion(ctx, args[0], version)
				if err != nil {
					return err
				}
				if asJSON {
					printJSON(os.Stdout, v)
					return nil
				}
				printVersion(v)
				return nil
			}
			vs, err := c.ListWorkflowVersions(ctx, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				printJSON(os.Stdout, vs)
				return nil
			}
			p := newPalette(os.Stdout)
			rows := [][]string{}
			for _, v := range vs {
				mark := ""
				if v.Current {
					mark = p.green("current")
				}
				rows = append(rows, []string{
					v.Version, mark, bundleText(v), age(v.Metadata.CreatedAt), age(v.ActivatedAt),
					strconv.Itoa(v.Runs.Total), phaseSummary(p, v.Runs.Phases), agePtr(v.Runs.LastRunAt),
				})
			}
			table(os.Stdout, []string{"VERSION", "STATUS", "CODE", "REGISTERED", "ACTIVATED", "RUNS", "OUTCOMES", "LAST RUN"}, rows)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return c
}

func newActivateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "activate <workflow> <version>",
		Short: "Make a registered version current (new runs, triggers and schedules use it)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return activate(cmd.Context(), args[0], args[1])
		},
	}
}

func newRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback <workflow> [version]",
		Short: "Make the previous version current again (or the given one)",
		Long: `Make an earlier version of a workflow current again. Without a version,
the one that was current before the current one is used.

Runs already started keep the version they started with; new runs, event
triggers and schedules declared in code use the activated version. Run
"flyke workflows rollback" again (or "activate") to roll forward.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 2 {
				return activate(cmd.Context(), args[0], args[1])
			}
			c, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			vs, err := c.ListWorkflowVersions(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if len(vs) < 2 {
				return fmt.Errorf("workflow %s has only one version; nothing to roll back to", args[0])
			}
			return activate(cmd.Context(), args[0], vs[1].Version)
		},
	}
}

func activate(ctx context.Context, workflow, version string) error {
	c, err := mustClient(ctx)
	if err != nil {
		return err
	}
	if version, err = resolveVersion(ctx, c, workflow, version); err != nil {
		return err
	}
	before, err := c.GetWorkflow(ctx, workflow)
	if err != nil {
		return err
	}
	wf, err := c.ActivateWorkflowVersion(ctx, workflow, version)
	if err != nil {
		return err
	}
	p := newPalette(os.Stderr)
	if before.Status.Version == wf.Status.Version {
		fmt.Fprintf(os.Stderr, "%s %s is already at %s\n", p.green("✓"), workflow, wf.Status.Version)
		return nil
	}
	fmt.Fprintf(os.Stderr, "%s %s: %s → %s\n", p.green("✓"), workflow, p.dim(before.Status.Version), p.bold(wf.Status.Version))
	fmt.Fprintln(os.Stderr, p.dim("  running runs keep their version; new runs use "+wf.Status.Version))
	return nil
}

// resolveVersion accepts a full version, or a unique prefix with or without "v-".
func resolveVersion(ctx context.Context, c *client.Client, workflow, v string) (string, error) {
	vs, err := c.ListWorkflowVersions(ctx, workflow)
	if err != nil {
		return "", err
	}
	want := strings.TrimPrefix(v, "v-")
	var match []string
	for _, x := range vs {
		if x.Version == v {
			return v, nil
		}
		if strings.HasPrefix(strings.TrimPrefix(x.Version, "v-"), want) {
			match = append(match, x.Version)
		}
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		return "", fmt.Errorf("workflow %s has no version %q (see: flyke workflows versions %s)", workflow, v, workflow)
	default:
		return "", fmt.Errorf("version %q is ambiguous: %s", v, strings.Join(match, ", "))
	}
}

func printVersion(v v1.WorkflowVersion) {
	p := newPalette(os.Stdout)
	cur := ""
	if v.Current {
		cur = " " + p.green("(current)")
	}
	fmt.Printf("%s %s%s\n", p.bold(v.Metadata.Name), v.Version, cur)
	kv := func(k, val string) { fmt.Printf("  %-12s %s\n", k+":", val) }
	kv("Registered", v.Metadata.CreatedAt.Local().Format(time.DateTime)+" ("+age(v.Metadata.CreatedAt)+" ago)")
	kv("Activated", v.ActivatedAt.Local().Format(time.DateTime)+" ("+age(v.ActivatedAt)+" ago)")
	kv("Entrypoint", v.Entrypoint)
	kv("Runtime", v.Runtime)
	if b := v.Bundle; b != nil {
		s := b.Digest
		if b.Size > 0 {
			s += fmt.Sprintf(", %.1f KiB", float64(b.Size)/1024)
		}
		if b.UploadedAt != nil {
			s += ", uploaded " + b.UploadedAt.Local().Format(time.DateTime)
		}
		kv("Bundle", s)
	} else {
		kv("Code", "local "+v.Source.LocalRoot)
	}
	kv("Tasks", strings.Join(v.Tasks, ", "))
	runs := fmt.Sprintf("%d", v.Runs.Total)
	if v.Runs.Total > 0 {
		runs += "  " + phaseSummary(p, v.Runs.Phases)
		runs += fmt.Sprintf("  (first %s ago, last %s ago)", agePtr(v.Runs.FirstRunAt), agePtr(v.Runs.LastRunAt))
	}
	kv("Runs", runs)
	if v.Runs.Total > 0 {
		fmt.Println(p.dim("\n  flyke runs ls --workflow " + v.Metadata.Name + " --version " + v.Version))
	}
	if !v.Current {
		fmt.Println(p.dim("  flyke workflows activate " + v.Metadata.Name + " " + v.Version + "   # make it current"))
	}
}

func bundleText(v v1.WorkflowVersion) string {
	if v.Bundle == nil {
		return "local"
	}
	d := strings.TrimPrefix(v.Bundle.Digest, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return "bundle " + d
}

func phaseSummary(p palette, phases map[string]int) string {
	keys := make([]string, 0, len(phases))
	for k := range phases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", p.phase(k), phases[k]))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func agePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return age(*t)
}
