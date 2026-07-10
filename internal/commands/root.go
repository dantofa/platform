// Package commands is the presentation layer: the cobra command tree. Commands
// only parse flags, call the framework-free core (through interfaces the client
// adapters satisfy), and render results via internal/render.
package commands

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dantofa/platform/internal/version"
)

// errHandled marks an error whose output has already been rendered (via
// render.Error) so the top-level Execute doesn't print it again — it only sets
// the non-zero exit code.
var errHandled = errors.New("handled")

// NewRootCmd builds the dctl root command tree. Both resource groups are always
// present: the Nix package bundles the local group's runtime CLIs (kind/flux/
// docker) on PATH, so `local` never needs to be hidden — if a tool is somehow
// missing, the command surfaces a clear "not installed" error instead.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "dctl",
		Short:         "dantofa platform control",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")

	root.AddCommand(newDOCmd())
	root.AddCommand(newLocalCmd())
	return root
}

// Execute runs the root command and returns the process exit code.
func Execute() int {
	if err := NewRootCmd().Execute(); err != nil {
		if !errors.Is(err, errHandled) {
			// A cobra/usage error that a command didn't render itself.
			fmt.Fprintln(os.Stderr, err)
		}
		return 1
	}
	return 0
}
