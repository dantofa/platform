// Package commands is the presentation layer: the cobra command tree. Commands
// only parse flags, call the framework-free core (through interfaces the client
// adapters satisfy), and render results via internal/render.
package commands

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/dantofa/platform/internal/version"
)

// errHandled marks an error whose output has already been rendered (via
// render.Error) so the top-level Execute doesn't print it again — it only sets
// the non-zero exit code.
var errHandled = errors.New("handled")

// NewRootCmd builds the dctl root command tree. The DigitalOcean group is always
// present; the local group is registered only when kind is on PATH.
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
	if executableAvailable("kind") {
		root.AddCommand(newLocalCmd())
	}
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

// executableAvailable reports whether name resolves on PATH.
func executableAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
