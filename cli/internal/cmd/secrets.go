package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
)

func newSecretsCmd() *cobra.Command {
	c := &cobra.Command{
		Use: "secrets", Aliases: []string{"secret", "vars"},
		Short: "Manage secrets and variables read by tasks with flyke.secrets",
		Long: `Secrets are key/value pairs that tasks read at run time:

    from flyke import secrets
    password = secrets["infoblox_password"]

A secret is Private to a namespace (default) or Shared by all namespaces
(--shared); a Private secret overrides a Shared one with the same key.

Mark passwords, tokens and keys --sensitive: they are encrypted at rest, their
value is never shown by the API, CLI or UI, and workers mask them in task logs.
Without --sensitive a secret is a plain variable whose value is visible.`,
	}
	var shared bool
	c.PersistentFlags().BoolVar(&shared, "shared", false, "address the Shared secrets (visible from every namespace)")

	var asJSON bool
	ls := &cobra.Command{
		Use: "ls", Short: "List secrets (the namespace's plus Shared ones, or only Shared with --shared)",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			items, err := cl.ListSecrets(cmd.Context(), shared)
			if err != nil {
				return err
			}
			if asJSON {
				printJSON(os.Stdout, items)
				return nil
			}
			p := newPalette(os.Stdout)
			overridden := map[string]bool{}
			for _, s := range items {
				if s.Type == "Private" {
					overridden[s.Key] = true
				}
			}
			rows := [][]string{}
			for _, s := range items {
				scope := s.Type
				if s.Type == "Shared" && overridden[s.Key] {
					scope += p.dim(" (overridden)")
				}
				rows = append(rows, []string{s.Key, scope, secretKind(s), secretValue(p, s), age(s.UpdatedAt)})
			}
			table(os.Stdout, []string{"KEY", "TYPE", "KIND", "VALUE", "UPDATED"}, rows)
			return nil
		},
	}
	ls.Flags().BoolVar(&asJSON, "json", false, "print JSON")

	get := &cobra.Command{
		Use: "get <key>", Short: "Show the secret a run in the namespace sees for key", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			s, err := cl.GetSecret(cmd.Context(), shared, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				printJSON(os.Stdout, s)
				return nil
			}
			p := newPalette(os.Stdout)
			scope := s.Type
			if s.Namespace != "" {
				scope += " (namespace " + s.Namespace + ")"
			}
			fmt.Printf("key:      %s\ntype:     %s\nkind:     %s\nvalue:    %s\nupdated:  %s ago\n",
				s.Key, scope, secretKind(s), secretValue(p, s), age(s.UpdatedAt))
			return nil
		},
	}
	get.Flags().BoolVar(&asJSON, "json", false, "print JSON")

	var sensitive, fromStdin bool
	var fromFile string
	set := &cobra.Command{
		Use:   "set <key> [value]",
		Short: "Create or update a secret",
		Long: `Create or update a secret. The value comes from the argument, --from-file,
--stdin, or (for --sensitive secrets on a terminal) a hidden prompt. Prefer
the prompt or --stdin for sensitive values: arguments end up in shell history.`,
		Example: `  flyke secrets set infoblox_username admin
  flyke secrets set infoblox_password --sensitive          # prompts, input hidden
  vault read -field=pw kv/db | flyke secrets set db_password --sensitive --stdin
  flyke secrets set smtp_host smtp.example.com --shared    # visible from all namespaces`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			value, err := secretInput(args, fromFile, fromStdin, sensitive)
			if err != nil {
				return err
			}
			spec := v1.SecretSpec{Value: &value}
			if cmd.Flags().Changed("sensitive") {
				spec.Sensitive = &sensitive
			}
			s, err := cl.PutSecret(cmd.Context(), shared, args[0], spec)
			if err != nil {
				return err
			}
			where := "namespace " + cl.Namespace
			if s.Type == "Shared" {
				where = "all namespaces"
			}
			fmt.Printf("secret/%s configured (%s, %s)\n", s.Key, strings.ToLower(secretKind(s)), where)
			return nil
		},
	}
	set.Flags().BoolVar(&sensitive, "sensitive", false, "encrypt the value and never display it (passwords, tokens…)")
	set.Flags().BoolVar(&fromStdin, "stdin", false, "read the value from standard input")
	set.Flags().StringVar(&fromFile, "from-file", "", "read the value from a file")

	rm := &cobra.Command{
		Use: "rm <key>...", Aliases: []string{"delete"}, Short: "Delete secrets", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := mustClient(cmd.Context())
			if err != nil {
				return err
			}
			for _, k := range args {
				if err := cl.DeleteSecret(cmd.Context(), shared, k); err != nil {
					return fmt.Errorf("%s: %w", k, err)
				}
				fmt.Printf("secret/%s deleted\n", k)
			}
			return nil
		},
	}
	c.AddCommand(ls, get, set, rm)
	return c
}

func secretKind(s v1.Secret) string {
	if s.Sensitive {
		return "Sensitive"
	}
	return "Variable"
}

func secretValue(p palette, s v1.Secret) string {
	if s.Sensitive {
		return p.dim("<hidden>")
	}
	if len(s.Value) > 60 {
		return s.Value[:57] + "..."
	}
	return s.Value
}

// secretInput returns the value from exactly one source.
func secretInput(args []string, fromFile string, fromStdin, sensitive bool) (string, error) {
	sources := 0
	for _, set := range []bool{len(args) == 2, fromFile != "", fromStdin} {
		if set {
			sources++
		}
	}
	if sources > 1 {
		return "", errors.New("give the value as an argument, --from-file or --stdin, not several")
	}
	switch {
	case len(args) == 2:
		return args[1], nil
	case fromFile != "":
		b, err := os.ReadFile(fromFile)
		return strings.TrimRight(string(b), "\r\n"), err
	case fromStdin:
		b, err := io.ReadAll(os.Stdin)
		return strings.TrimRight(string(b), "\r\n"), err
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("missing value: pass it as an argument, --from-file or --stdin")
	}
	fmt.Fprintf(os.Stderr, "value for %s: ", args[0])
	if sensitive {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
