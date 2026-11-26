package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
	"github.com/flyke-org/flyke/cli/internal/bundle"
	"github.com/flyke-org/flyke/cli/internal/client"
	"github.com/flyke-org/flyke/cli/internal/local"
	"github.com/flyke-org/flyke/ui"
)

type runOpts struct {
	follow     bool
	detach     bool
	forceBundl bool
	root       string
	target     string
	inputs     []string
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <file.py:Workflow> [--input=value ...] [-f]",
		Short: "Run a workflow (starts a local control plane + worker if none is configured)",
		Long: `Run a workflow and wait for it to finish.

Workflow inputs are passed as flags named after the workflow's fields:

  flyke run examples/deploy.py:DeployService --service=api --env=prod -f

Flags: -f/--follow streams logs; -d/--detach returns after starting (requires
a running control plane); --root DIR sets the code root (default: the file's
directory); --bundle forces uploading code even to a local control plane.

Without --server / FLYKE_SERVER and without a running local control plane,
an ephemeral one is started in-process together with a local worker. Both
speak exactly the same protocol as a production deployment.`,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := parseRunArgs(args)
			if err != nil {
				return err
			}
			if opts.target == "" {
				return cmd.Help()
			}
			return runWorkflow(cmd.Context(), opts)
		},
	}
}

func parseRunArgs(args []string) (runOpts, error) {
	var o runOpts
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "-") {
				return v, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", a)
			}
			i++
			return args[i], nil
		}
		name, _, _ := strings.Cut(a, "=")
		var err error
		switch name {
		case "-h", "--help":
			return runOpts{}, nil
		case "-f", "--follow":
			o.follow = true
		case "-d", "--detach":
			o.detach = true
		case "--bundle":
			o.forceBundl = true
		case "--debug":
			g.debug = true
		case "--server":
			g.server, err = next()
		case "--token":
			g.token, err = next()
		case "-n", "--namespace":
			g.namespace, err = next()
		case "--root":
			o.root, err = next()
		default:
			switch {
			case strings.HasPrefix(a, "--"):
				if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					a = a + "=" + args[i+1]
					i++
				}
				o.inputs = append(o.inputs, strings.TrimPrefix(a, "--"))
			case o.target == "":
				o.target = a
			default:
				return o, fmt.Errorf("unexpected argument %q", a)
			}
		}
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

// buildInputs coerces CLI strings using the workflow's JSON schema.
func buildInputs(schemaRaw json.RawMessage, raw []string) (json.RawMessage, error) {
	var schema struct {
		Properties map[string]struct {
			Type  any `json:"type"`
			Items *struct {
				Type string `json:"type"`
			} `json:"items"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(schemaRaw, &schema)
	inputs := map[string]any{}
	for _, kv := range raw {
		key, val, hasVal := strings.Cut(kv, "=")
		key = strings.ReplaceAll(key, "-", "_")
		if !hasVal {
			if strings.HasPrefix(key, "no_") {
				if _, ok := schema.Properties[key]; !ok {
					inputs[strings.TrimPrefix(key, "no_")] = false
					continue
				}
			}
			val = "true"
		}
		prop, ok := schema.Properties[key]
		if !ok {
			names := make([]string, 0, len(schema.Properties))
			for n := range schema.Properties {
				names = append(names, "--"+n)
			}
			return nil, fmt.Errorf("unknown input --%s (inputs: %s)", key, strings.Join(names, ", "))
		}
		typ, _ := prop.Type.(string)
		v, err := coerce(typ, prop.Items, val)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", key, err)
		}
		inputs[key] = v
	}
	return json.Marshal(inputs)
}

func coerce(typ string, items *struct {
	Type string `json:"type"`
}, val string) (any, error) {
	trimmed := strings.TrimSpace(val)
	switch typ {
	case "boolean":
		return strconv.ParseBool(trimmed)
	case "integer":
		return strconv.ParseInt(trimmed, 10, 64)
	case "number":
		return strconv.ParseFloat(trimmed, 64)
	case "array":
		if strings.HasPrefix(trimmed, "[") {
			var out []any
			return out, json.Unmarshal([]byte(trimmed), &out)
		}
		var out []any
		for _, part := range strings.Split(val, ",") {
			itemType := ""
			if items != nil {
				itemType = items.Type
			}
			v, err := coerce(itemType, nil, strings.TrimSpace(part))
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case "object":
		var out map[string]any
		return out, json.Unmarshal([]byte(trimmed), &out)
	case "string":
		return val, nil
	default:
		var out any
		if json.Unmarshal([]byte(trimmed), &out) == nil {
			return out, nil
		}
		return val, nil
	}
}

func runWorkflow(parent context.Context, o runOpts) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	p := newPalette(os.Stdout)

	file, _, _ := strings.Cut(o.target, ":")
	python, err := local.FindPython(filepath.Dir(file))
	if err != nil {
		return err
	}
	m, err := local.ExtractManifest(ctx, python, o.target, o.root)
	if err != nil {
		return err
	}
	var meta struct {
		Name         string          `json:"name"`
		Entrypoint   string          `json:"entrypoint"`
		InputsSchema json.RawMessage `json:"inputs_schema"`
	}
	if err := json.Unmarshal(m.Manifest, &meta); err != nil {
		return err
	}
	inputs, err := buildInputs(meta.InputsSchema, o.inputs)
	if err != nil {
		return err
	}

	c, remote := resolveServer(ctx)
	var cluster *local.Cluster
	if !remote {
		if o.detach {
			return errors.New("--detach needs a running control plane (flyke server --dev, or --server)")
		}
		var workerOut *os.File
		if g.debug {
			workerOut = os.Stderr
		}
		opts := local.Options{Python: python, WithWorker: true, UI: ui.FS()}
		if workerOut != nil {
			opts.WorkerOutput = workerOut
		}
		cluster, err = local.Start(context.WithoutCancel(ctx), opts)
		if err != nil {
			return fmt.Errorf("start local control plane: %w", err)
		}
		defer cluster.Stop()
		c = client.New(cluster.Server.URL(), "", g.namespace)
		fmt.Fprintln(os.Stderr, p.dim(fmt.Sprintf("local control plane on %s (ephemeral) - 1 local worker", cluster.Server.URL())))
	}

	src := v1.Source{Entrypoint: meta.Entrypoint}
	// A local control plane reads the code in place, which only the admin
	// may ask for: with a namespace access token the code ships as a bundle.
	inPlace := !o.forceBundl && isLoopback(c.BaseURL)
	if inPlace && remote {
		if me, err := c.WhoAmI(ctx); err == nil && !me.Admin {
			inPlace = false
		}
	}
	if inPlace {
		src.LocalRoot = m.Root
	} else {
		data, err := bundle.Pack(m.Root)
		if err != nil {
			return fmt.Errorf("bundle %s: %w", m.Root, err)
		}
		up, err := c.UploadBundle(ctx, bytes.NewReader(data))
		if err != nil {
			return err
		}
		src.BundleDigest = up.Digest
	}
	wf, err := c.PutWorkflow(ctx, meta.Name, v1.WorkflowSpec{Manifest: m.Manifest, Source: src})
	if err != nil {
		return err
	}
	run, err := c.StartRun(ctx, wf.Metadata.Name, v1.StartRunRequest{Inputs: inputs})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s %s %s\n", p.bold("▶"), p.bold(run.Metadata.Name), p.dim("("+wf.Metadata.Name+" "+wf.Status.Version+")"))
	if cluster == nil || cluster.Server.HasUI() {
		fmt.Fprintln(os.Stderr, p.dim("  "+strings.TrimRight(c.BaseURL, "/")+"/runs/"+run.Metadata.Name))
	}
	if o.detach {
		fmt.Println(run.Metadata.Name)
		return nil
	}
	return followRun(ctx, c, run.Metadata.Name, o.follow, cluster)
}

// followRun waits for the run to end, optionally printing its logs.
func followRun(ctx context.Context, c *client.Client, id string, showLogs bool, cluster *local.Cluster) error {
	p := newPalette(os.Stdout)
	start := time.Now()
	var final *v1.Run
	var lastHint string
	err := c.FollowLogs(ctx, id, 0, func(ev client.LogEvent) error {
		if ev.End != nil {
			final = ev.End
			return nil
		}
		e := *ev.Log
		if showLogs {
			printLog(os.Stdout, p, e, g.debug)
		} else if e.Source == "system" && (e.Level == "error" || strings.Contains(e.Message, "waiting for event") || strings.Contains(e.Message, "paused")) {
			printLog(os.Stdout, p, e, false)
		}
		if strings.Contains(e.Message, "run paused") || strings.Contains(e.Message, "— run paused") {
			if hint := extractHint(e); hint != "" && hint != lastHint {
				lastHint = hint
				fmt.Fprintln(os.Stderr, p.yellow("⏸  run paused — "+hint))
			}
		}
		return nil
	})
	if ctx.Err() != nil {
		if cluster != nil {
			fmt.Fprintln(os.Stderr, p.yellow("\ninterrupted: the local control plane is shutting down; the run is abandoned"))
		} else {
			fmt.Fprintf(os.Stderr, "\n%s still running on the control plane; follow with `flyke logs -f %s`\n", id, id)
		}
		return exitError{130}
	}
	if err != nil {
		return err
	}
	if final == nil {
		r, err := c.GetRun(context.Background(), id)
		if err != nil {
			return err
		}
		final = &r
	}
	return printOutcome(*final, time.Since(start))
}

func extractHint(e v1.LogEntry) string {
	var f struct {
		Hint string `json:"hint"`
	}
	_ = json.Unmarshal(e.Fields, &f)
	return f.Hint
}

func printOutcome(r v1.Run, elapsed time.Duration) error {
	p := newPalette(os.Stdout)
	st := r.Status
	line := fmt.Sprintf("%s %s in %s", p.bold(r.Metadata.Name), p.phase(st.Phase), elapsed.Round(10*time.Millisecond))
	if st.Compensation != "" {
		line += p.dim(" (compensation " + strings.ToLower(st.Compensation) + ")")
	}
	fmt.Println()
	fmt.Println(line)
	switch st.Phase {
	case "Succeeded":
		if len(st.Result) > 0 && string(st.Result) != "null" {
			fmt.Println(prettyJSON(st.Result))
		}
		return nil
	default:
		if st.Message != "" {
			fmt.Println(p.red(st.Message))
		}
		return exitError{1}
	}
}
