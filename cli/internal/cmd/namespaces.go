package cmd

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
)

// orShared renders the namespace of a pool or worker.
func orShared(ns string) string {
	if ns == "" {
		return "(shared)"
	}
	return ns
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// ------------------------------------------------------------ namespaces ----

func newNamespacesCmd() *cobra.Command {
	c := &cobra.Command{
		Use: "namespaces", Aliases: []string{"namespace", "ns"},
		Short: "Manage namespaces (tenants). Creating, updating and deleting need the admin token",
		Long: `A namespace is a tenant: its workflows, runs, events, secrets, code bundles,
pools, workers and access tokens are invisible to every other namespace.

The admin token manages all namespaces. A team works with an access token
(flyke tokens create), which only works inside its own namespace.

A namespace's code runs on its own workers (flyke -n <ns> pools token <pool>).
With --shared-workers it may also run on the shared pools the admin operates.`,
	}
	ls := &cobra.Command{
		Use: "ls", Short: "List namespaces (all of them for the admin, otherwise your own)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			nss, err := cl.ListNamespaces(cmd.Context())
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, n := range nss {
				rows = append(rows, []string{n.Metadata.Name, yesNo(n.Spec.SharedWorkers), strconv.Itoa(n.Status.Workflows),
					strconv.Itoa(n.Status.ActiveRuns), strconv.Itoa(n.Status.Workers), strconv.Itoa(n.Status.Tokens),
					age(n.Metadata.CreatedAt), n.Spec.Description})
			}
			table(os.Stdout, []string{"NAMESPACE", "SHARED WORKERS", "WORKFLOWS", "ACTIVE RUNS", "WORKERS", "TOKENS", "AGE", "DESCRIPTION"}, rows)
			return nil
		},
	}

	var description string
	var sharedWorkers bool
	create := &cobra.Command{
		Use: "create <name>", Short: "Create a namespace", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			n, err := cl.CreateNamespace(cmd.Context(), v1.CreateNamespaceRequest{
				Name: args[0], NamespaceSpec: v1.NamespaceSpec{Description: description, SharedWorkers: sharedWorkers},
			})
			if err != nil {
				return err
			}
			fmt.Printf("namespace/%s created\n", n.Metadata.Name)
			fmt.Fprintf(os.Stderr, "next: flyke -n %s tokens create <name>   # an access token for the team\n", n.Metadata.Name)
			if !n.Spec.SharedWorkers {
				fmt.Fprintf(os.Stderr, "      flyke -n %s pools token <pool>     # a join token for the team's own workers\n", n.Metadata.Name)
			}
			return nil
		},
	}
	create.Flags().StringVar(&description, "description", "", "what the namespace is for")
	create.Flags().BoolVar(&sharedWorkers, "shared-workers", false, "let the namespace's work run on shared pools")

	update := &cobra.Command{
		Use: "update <name>", Short: "Change a namespace's description or shared-workers setting", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			cur, err := cl.GetNamespace(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			spec := cur.Spec
			if cmd.Flags().Changed("description") {
				spec.Description = description
			}
			if cmd.Flags().Changed("shared-workers") {
				spec.SharedWorkers = sharedWorkers
			}
			if _, err := cl.UpdateNamespace(cmd.Context(), args[0], spec); err != nil {
				return err
			}
			fmt.Printf("namespace/%s updated\n", args[0])
			return nil
		},
	}
	update.Flags().StringVar(&description, "description", "", "what the namespace is for")
	update.Flags().BoolVar(&sharedWorkers, "shared-workers", false, "let the namespace's work run on shared pools (--shared-workers=false to stop)")

	var yes bool
	rm := &cobra.Command{
		Use: "rm <name>", Aliases: []string{"delete"}, Args: cobra.ExactArgs(1),
		Short: "Delete a namespace and everything in it (runs, workflows, secrets, tokens, pools, workers)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			n, err := cl.GetNamespace(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !yes {
				return fmt.Errorf("this permanently deletes namespace %s with %d workflow(s), %d active run(s), %d worker(s) and %d token(s); pass --yes to confirm",
					args[0], n.Status.Workflows, n.Status.ActiveRuns, n.Status.Workers, n.Status.Tokens)
			}
			if err := cl.DeleteNamespace(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("namespace/%s deleted\n", args[0])
			return nil
		},
	}
	rm.Flags().BoolVar(&yes, "yes", false, "confirm the deletion")
	c.AddCommand(ls, create, update, rm)
	return c
}

// ---------------------------------------------------------------- tokens ----

func newTokensCmd() *cobra.Command {
	c := &cobra.Command{
		Use: "tokens", Aliases: []string{"token"},
		Short: "Manage the namespace's access tokens (API credentials that only work in that namespace)",
	}
	ls := &cobra.Command{
		Use: "ls", Short: "List access tokens",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			toks, err := cl.ListAccessTokens(cmd.Context())
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, t := range toks {
				exp := "never"
				if t.ExpiresAt != nil {
					exp = t.ExpiresAt.Local().Format("2006-01-02 15:04")
					if time.Now().After(*t.ExpiresAt) {
						exp += " (expired)"
					}
				}
				rows = append(rows, []string{t.ID, t.Name, age(t.CreatedAt), exp})
			}
			table(os.Stdout, []string{"ID", "NAME", "AGE", "EXPIRES"}, rows)
			return nil
		},
	}
	var ttl time.Duration
	create := &cobra.Command{
		Use: "create <name>", Short: "Create an access token for the namespace (printed once)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ttl < 0 {
				return errors.New("--ttl must not be negative")
			}
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			t, err := cl.CreateAccessToken(cmd.Context(), v1.CreateAccessTokenRequest{Name: args[0], TTLSeconds: int(ttl / time.Second)})
			if err != nil {
				return err
			}
			fmt.Println(t.Token)
			fmt.Fprintf(os.Stderr, "access token %s for namespace %s; it is not shown again.\nuse it with: FLYKE_TOKEN=<token> FLYKE_NAMESPACE=%s flyke …\n",
				t.ID, t.Namespace, t.Namespace)
			return nil
		},
	}
	create.Flags().DurationVar(&ttl, "ttl", 0, "lifetime, e.g. 720h (default: never expires)")
	rm := &cobra.Command{
		Use: "rm <id>...", Aliases: []string{"revoke", "delete"}, Short: "Revoke access tokens", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			for _, id := range args {
				if err := cl.DeleteAccessToken(cmd.Context(), id); err != nil {
					return fmt.Errorf("%s: %w", id, err)
				}
				fmt.Printf("token/%s revoked\n", id)
			}
			return nil
		},
	}
	c.AddCommand(ls, create, rm)
	return c
}

func newWhoAmICmd() *cobra.Command {
	return &cobra.Command{
		Use: "whoami", Short: "Show what the current token can access",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			me, err := cl.WhoAmI(cmd.Context())
			if err != nil {
				return err
			}
			switch {
			case !me.AuthRequired:
				fmt.Println("admin (this control plane has no admin token: the API is open)")
			case me.Admin:
				fmt.Println("admin (all namespaces)")
			default:
				fmt.Printf("namespace %s (access token %q)\n", me.Namespace, me.Token)
			}
			return nil
		},
	}
}
