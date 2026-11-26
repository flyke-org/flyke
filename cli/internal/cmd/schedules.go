package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
	"github.com/flyke-org/flyke/cli/internal/client"
)

// timing holds the flags that say when a schedule fires. Exactly one of
// them is given; all of them become a cron expression.
type timing struct {
	cron, every, daily string
}

func (t *timing) register(c *cobra.Command) {
	c.Flags().StringVar(&t.every, "every", "", `fixed interval, e.g. 15m, 2h (minimum 10s)`)
	c.Flags().StringVar(&t.daily, "daily", "", `once a day at HH:MM, e.g. 03:00`)
	c.Flags().StringVar(&t.cron, "cron", "", `cron expression "minute hour day-of-month month day-of-week", e.g. "0 9 * * 1-5", or @hourly, @daily, @weekly, @monthly`)
}

var clockRe = regexp.MustCompile(`^([01]?\d|2[0-3]):([0-5]\d)$`)

// expression returns the cron expression of the flags ("" if none was given).
func (t *timing) expression() (string, error) {
	given := 0
	for _, v := range []string{t.cron, t.every, t.daily} {
		if v != "" {
			given++
		}
	}
	switch {
	case given > 1:
		return "", errors.New("use only one of --every, --daily and --cron")
	case t.every != "":
		d, err := time.ParseDuration(t.every)
		if err != nil || d <= 0 {
			return "", fmt.Errorf("--every %q: want a duration such as 30s, 15m or 2h", t.every)
		}
		// 1h30m0s → 1h30m
		text := d.String()
		if strings.HasSuffix(text, "m0s") {
			text = strings.TrimSuffix(text, "0s")
		}
		if strings.HasSuffix(text, "h0m") {
			text = strings.TrimSuffix(text, "0m")
		}
		return "@every " + text, nil
	case t.daily != "":
		m := clockRe.FindStringSubmatch(t.daily)
		if m == nil {
			return "", fmt.Errorf("--daily %q: want a time of day as HH:MM, e.g. 03:00", t.daily)
		}
		hour, _ := strconv.Atoi(m[1])
		minute, _ := strconv.Atoi(m[2])
		return fmt.Sprintf("%d %d * * *", minute, hour), nil
	}
	return t.cron, nil
}

// until renders a future time relative to now: "in 5m", "in 3h", "in 2d".
func until(t time.Time) string {
	d := time.Until(t)
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("in %ds", int(d.Seconds())+1)
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("in %dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("in %dd", int(d.Hours()/24))
}

// zoned shows t in the schedule's own time zone, the one its cron
// expression is written in.
func zoned(t time.Time, tz string) string {
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	layout := "Mon 2 Jan 15:04 MST"
	if t.Second() != 0 {
		layout = "Mon 2 Jan 15:04:05 MST"
	}
	return t.In(loc).Format(layout)
}

func scheduleState(sc v1.Schedule) string {
	switch {
	case sc.Spec.Paused:
		return "Paused"
	case sc.Status.LastError != "":
		return "Error"
	}
	return "Active"
}

// printSchedule shows what a schedule does and when it runs next.
func printSchedule(w io.Writer, sc v1.Schedule) {
	p := newPalette(os.Stdout)
	origin := ""
	if sc.Status.Declared {
		origin = p.dim("  (declared in code)")
	}
	fmt.Fprintf(w, "%s  runs %s  %s%s\n", p.bold(sc.Metadata.Name), sc.Spec.Workflow, p.phase(scheduleState(sc)), origin)
	fmt.Fprintf(w, "  when:     %s  %s\n", sc.Status.Description, p.dim("["+sc.Spec.Cron+"]"))
	if len(sc.Spec.Inputs) > 0 {
		b, _ := json.Marshal(sc.Spec.Inputs)
		fmt.Fprintf(w, "  inputs:   %s\n", b)
	}
	if sc.Spec.Missed == "skip" {
		fmt.Fprintln(w, "  missed:   skip (a tick that cannot fire on time is dropped)")
	}
	if sc.Status.LastRun != "" {
		fmt.Fprintf(w, "  last run: %s  %s  %s ago\n", sc.Status.LastRun, p.phase(sc.Status.LastRunPhase), age(*sc.Status.LastRunAt))
	}
	if sc.Status.LastError != "" {
		fmt.Fprintf(w, "  %s the last tick did not start a run: %s\n", p.red("error:"), sc.Status.LastError)
	}
	if sc.Spec.Paused {
		fmt.Fprintf(w, "  paused: no run starts until `flyke schedules resume %s`\n", sc.Metadata.Name)
		return
	}
	printUpcoming(w, sc.Status.Upcoming, sc.Spec.TZ)
}

func printUpcoming(w io.Writer, ticks []time.Time, tz string) {
	if len(ticks) == 0 {
		return
	}
	fmt.Fprintln(w, "  next runs:")
	for _, t := range ticks {
		fmt.Fprintf(w, "    %s  (%s)\n", zoned(t, tz), until(t))
	}
}

// parseSet turns --set key=value flags into workflow inputs (JSON values allowed).
func parseSet(set []string, into map[string]any) (map[string]any, error) {
	if into == nil {
		into = map[string]any{}
	}
	for _, kv := range set {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("--set %q: want key=value", kv)
		}
		var val any
		if json.Unmarshal([]byte(v), &val) != nil {
			val = v
		}
		into[k] = val
	}
	return into, nil
}

func newSchedulesCmd() *cobra.Command {
	c := &cobra.Command{
		Use: "schedules", Aliases: []string{"schedule", "sched"},
		Short: "Run workflows on a schedule (every 15 minutes, daily at 03:00, any cron expression)",
		Long: `A schedule starts a run of a workflow at recurring times.

Say when with one of:
  --every 15m             a fixed interval
  --daily 03:00           once a day
  --cron "0 9 * * 1-5"    any cron expression (here: Monday to Friday at 09:00)

Times are read in --tz (an IANA zone such as Europe/Paris); the default is UTC.
Every command prints the schedule in plain English with its next runs, so you
can check that it means what you intended. "flyke schedules explain" does the
same without saving anything.

Schedules can also be declared in workflow code:

  @workflow(triggers=[on.schedule("0 3 * * *", tz="Europe/Paris")])

Those are created by "flyke deploy" and changed by deploying again; from here
they can be paused, resumed and triggered.`,
		Example: `  flyke schedules create nightly-report Report --daily 03:00 --tz Europe/Paris
  flyke schedules create sync Sync --every 15m --set source=crm
  flyke schedules create weekdays Digest --cron "0 9 * * 1-5" --set day={schedule.date}
  flyke schedules explain "0 9 * * 1-5" --tz Europe/Paris
  flyke schedules ls
  flyke schedules pause nightly-report
  flyke schedules trigger nightly-report     # run it now`,
	}

	ls := &cobra.Command{
		Use: "ls", Short: "List schedules with their next run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			workflow, _ := cmd.Flags().GetString("workflow")
			scs, err := cl.ListSchedules(cmd.Context(), workflow)
			if err != nil {
				return err
			}
			if len(scs) == 0 {
				fmt.Fprintln(os.Stderr, "no schedules yet. Create one with: flyke schedules create <name> <workflow> --every 15m")
				return nil
			}
			rows := [][]string{}
			for _, sc := range scs {
				next, last := "-", "-"
				if sc.Status.NextRunAt != nil {
					next = until(*sc.Status.NextRunAt)
				}
				if sc.Status.LastRunAt != nil {
					last = age(*sc.Status.LastRunAt) + " ago"
					if sc.Status.LastRunPhase != "" {
						last += " (" + sc.Status.LastRunPhase + ")"
					}
				}
				rows = append(rows, []string{sc.Metadata.Name, sc.Spec.Workflow, sc.Status.Description, scheduleState(sc), next, last})
			}
			table(os.Stdout, []string{"SCHEDULE", "WORKFLOW", "WHEN", "STATE", "NEXT RUN", "LAST RUN"}, rows)
			return nil
		},
	}
	ls.Flags().String("workflow", "", "only the schedules of this workflow")

	get := &cobra.Command{
		Use: "get <schedule>", Short: "Show a schedule and its next runs", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			sc, err := cl.GetSchedule(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printSchedule(os.Stdout, sc)
			return nil
		},
	}

	var when timing
	var tz, missed string
	var set []string
	var paused bool
	specFlags := func(c *cobra.Command) {
		when.register(c)
		c.Flags().StringVar(&tz, "tz", "", "time zone the schedule is read in, e.g. Europe/Paris (default UTC)")
		c.Flags().StringArrayVar(&set, "set", nil, "workflow input as key=value (repeatable); values may use {schedule.date}, {schedule.time}, {schedule.name}")
		c.Flags().StringVar(&missed, "missed", "", `a tick missed while the server was down: "run_once" (default: run once on restart) or "skip"`)
	}

	create := &cobra.Command{
		Use: "create <name> <workflow> (--every 15m | --daily 03:00 | --cron \"0 9 * * 1-5\")", Args: cobra.ExactArgs(2),
		Short: "Create a schedule for a deployed workflow",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			expr, err := when.expression()
			if err != nil {
				return err
			}
			if expr == "" {
				return errors.New(`say when it should run: --every 15m, --daily 03:00 or --cron "0 9 * * 1-5"`)
			}
			inputs, err := parseSet(set, nil)
			if err != nil {
				return err
			}
			cl, err := mustClient(ctx)
			if err != nil {
				return err
			}
			var apiErr *client.APIError
			if _, err := cl.GetSchedule(ctx, args[0]); err == nil {
				return fmt.Errorf("schedule %s already exists; change it with `flyke schedules update %s`", args[0], args[0])
			} else if !errors.As(err, &apiErr) || apiErr.Status != 404 {
				return err
			}
			sc, err := cl.PutSchedule(ctx, args[0], v1.ScheduleSpec{
				Workflow: args[1], Cron: expr, TZ: tz, Inputs: inputs, Missed: missed, Paused: paused,
			})
			if err != nil {
				return err
			}
			fmt.Printf("schedule/%s created\n", sc.Metadata.Name)
			printSchedule(os.Stdout, sc)
			return nil
		},
	}
	specFlags(create)
	create.Flags().BoolVar(&paused, "paused", false, "create it paused; start it later with `flyke schedules resume`")

	update := &cobra.Command{
		Use: "update <schedule>", Args: cobra.ExactArgs(1),
		Short: "Change when a schedule runs, its time zone, inputs or missed policy (only what you pass changes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			expr, err := when.expression()
			if err != nil {
				return err
			}
			cl, err := mustClient(ctx)
			if err != nil {
				return err
			}
			cur, err := cl.GetSchedule(ctx, args[0])
			if err != nil {
				return err
			}
			spec := cur.Spec
			if expr != "" {
				spec.Cron = expr
			}
			if cmd.Flags().Changed("tz") {
				spec.TZ = tz
			}
			if cmd.Flags().Changed("missed") {
				spec.Missed = missed
			}
			if spec.Inputs, err = parseSet(set, spec.Inputs); err != nil {
				return err
			}
			sc, err := cl.PutSchedule(ctx, args[0], spec)
			if err != nil {
				return err
			}
			fmt.Printf("schedule/%s updated\n", sc.Metadata.Name)
			printSchedule(os.Stdout, sc)
			return nil
		},
	}
	specFlags(update)

	action := func(use, verb, done, short string) *cobra.Command {
		return &cobra.Command{
			Use: use + " <schedule>...", Short: short, Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cl, err := mustClient(cmd.Context())
				if err != nil {
					return err
				}
				for _, name := range args {
					sc, err := cl.ScheduleAction(cmd.Context(), name, verb)
					if err != nil {
						return fmt.Errorf("%s: %w", name, err)
					}
					line := fmt.Sprintf("schedule/%s %s", name, done)
					if sc.Status.NextRunAt != nil {
						line += fmt.Sprintf("; next run %s (%s)", zoned(*sc.Status.NextRunAt, sc.Spec.TZ), until(*sc.Status.NextRunAt))
					}
					fmt.Println(line)
				}
				return nil
			},
		}
	}
	pause := action("pause", "pause", "paused", "Stop a schedule from starting runs until it is resumed")
	resume := action("resume", "resume", "resumed", "Resume a paused schedule from its next tick (ticks missed while paused are not run)")

	trigger := &cobra.Command{
		Use: "trigger <schedule>", Aliases: []string{"run"}, Args: cobra.ExactArgs(1),
		Short: "Start a run of a schedule now, with its inputs (its timetable does not change)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			r, err := cl.TriggerSchedule(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("run/%s started\n", r.Metadata.Name)
			fmt.Fprintf(os.Stderr, "follow it with: flyke logs -f %s\n", r.Metadata.Name)
			return nil
		},
	}

	rm := &cobra.Command{
		Use: "rm <schedule>...", Aliases: []string{"delete"}, Short: "Delete schedules (runs they started are kept)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			for _, name := range args {
				if err := cl.DeleteSchedule(cmd.Context(), name); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				fmt.Printf("schedule/%s deleted\n", name)
			}
			return nil
		},
	}

	var explainWhen timing
	var explainTZ string
	explain := &cobra.Command{
		Use: "explain [cron expression]", Args: cobra.MaximumNArgs(1),
		Short: "Show what a cron expression means and when it fires next, without saving anything",
		Example: `  flyke schedules explain "0 9 * * 1-5" --tz Europe/Paris
  flyke schedules explain --every 90m`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if explainWhen.cron != "" {
					return errors.New("pass the cron expression once, as the argument or with --cron")
				}
				explainWhen.cron = args[0]
			}
			expr, err := explainWhen.expression()
			if err != nil {
				return err
			}
			if expr == "" {
				return errors.New(`pass a cron expression, e.g. flyke schedules explain "0 9 * * 1-5"`)
			}
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			p, err := cl.PreviewSchedule(cmd.Context(), v1.SchedulePreviewRequest{Cron: expr, TZ: explainTZ})
			if err != nil {
				return err
			}
			fmt.Printf("%s  %s\n", p.Description, newPalette(os.Stdout).dim("["+expr+"]"))
			printUpcoming(os.Stdout, p.Upcoming, explainTZ)
			return nil
		},
	}
	explainWhen.register(explain)
	explain.Flags().StringVar(&explainTZ, "tz", "", "time zone, e.g. Europe/Paris (default UTC)")

	c.AddCommand(ls, get, create, update, pause, resume, trigger, rm, explain)
	return c
}
