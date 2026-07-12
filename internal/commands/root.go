// Package commands is the presentation layer: the cobra command tree. Commands
// only parse flags, call the framework-free core (through interfaces the client
// adapters satisfy), and render results via internal/render. Each resource
// group lives in its own subpackage (digitalocean/, local/); this package only
// assembles the root and runs it.
package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/dantofa/platform/internal/commands/digitalocean"
	fluxcmd "github.com/dantofa/platform/internal/commands/flux"
	"github.com/dantofa/platform/internal/commands/local"
	"github.com/dantofa/platform/internal/render"
	"github.com/dantofa/platform/internal/version"
)

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

	root.AddCommand(digitalocean.NewCmd())
	root.AddCommand(fluxcmd.NewCmd())
	root.AddCommand(local.NewCmd())
	return root
}

// Execute runs the root command and returns the process exit code. The context
// is cancelled on SIGINT/SIGTERM, so an interrupt (Ctrl-C) propagates through
// cmd.Context() to the exec.CommandContext calls in the client adapters — a
// long child like `kind create` is signalled instead of being orphaned.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := NewRootCmd().ExecuteContext(ctx); err != nil {
		if !errors.Is(err, render.ErrHandled) {
			// A cobra/usage error that a command didn't render itself.
			fmt.Fprintln(os.Stderr, err)
		}
		return 1
	}
	return 0
}
