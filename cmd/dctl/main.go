// Command dctl is the dantofa platform control CLI.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dantofa/platform/internal/version"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "dctl",
		Short:         "dantofa platform control",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// No subcommands yet (Phase 2). Show help until they land.
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
