// Command flyke is the Flyke command line: run workflows locally or against a
// control plane, inspect runs, stream logs, and manage workers and pools.
package main

import (
	"os"

	"github.com/flyke-org/flyke/cli/internal/cmd"
)

func main() { os.Exit(cmd.Execute()) }
