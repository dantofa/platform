// Package flux holds the `flux` command group: compose GitOps sources and
// kustomizations on a cluster via the bundled flux CLI. The group is
// provider-agnostic (works on any kubeconfig, kind or DOKS); to target a DOKS
// cluster, fetch its kubeconfig first with `dctl do cluster connect`.
package flux

import (
	"github.com/spf13/cobra"

	fluxclient "github.com/dantofa/platform/internal/clients/flux"
	fluxcore "github.com/dantofa/platform/internal/core/flux"
	"github.com/dantofa/platform/internal/render"
)

// NewCmd builds the `flux` resource group. A persistent --kubeconfig selects
// the target cluster (empty defers to $KUBECONFIG / ~/.kube/config).
func NewCmd() *cobra.Command {
	var kubeconfig string
	flux := &cobra.Command{
		Use:          "flux",
		Short:        "Compose Flux GitOps sources and kustomizations on a cluster.",
		SilenceUsage: true,
	}
	flux.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "",
		"Kubeconfig path (defaults to $KUBECONFIG / ~/.kube/config).")
	flux.AddCommand(newSourceCmd(&kubeconfig), newKustomizationCmd(&kubeconfig))
	return flux
}

func newSourceCmd(kubeconfig *string) *cobra.Command {
	source := &cobra.Command{
		Use:   "source",
		Short: "Manage Flux GitRepository sources.",
	}
	source.AddCommand(newSourceCreateCmd(kubeconfig), newSourceDeleteCmd(kubeconfig))
	return source
}

func newSourceCreateCmd(kubeconfig *string) *cobra.Command {
	var url, branch string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create or update a GitRepository source. Idempotent.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := fluxcore.AddSource(cmd.Context(), fluxclient.New(*kubeconfig),
				fluxcore.SourceSpec{Name: args[0], URL: url, Branch: branch})
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(res)
		},
	}
	f := cmd.Flags()
	f.StringVar(&url, "url", "", "Git URL of the source.")
	_ = cmd.MarkFlagRequired("url")
	f.StringVar(&branch, "branch", fluxcore.DefaultSourceBranch, "Branch to track.")
	return cmd
}

func newSourceDeleteCmd(kubeconfig *string) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a GitRepository source.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := fluxcore.RemoveSource(cmd.Context(), fluxclient.New(*kubeconfig), args[0]); err != nil {
				return render.Fail(err)
			}
			return render.JSON(map[string]string{"deleted_source": args[0]})
		},
	}
}

func newKustomizationCmd(kubeconfig *string) *cobra.Command {
	ks := &cobra.Command{
		Use:     "kustomization",
		Aliases: []string{"ks"},
		Short:   "Manage Flux Kustomizations.",
	}
	ks.AddCommand(newKustomizationCreateCmd(kubeconfig), newKustomizationDeleteCmd(kubeconfig))
	return ks
}

func newKustomizationCreateCmd(kubeconfig *string) *cobra.Command {
	var source, path string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create or update a Kustomization. Idempotent.",
		Long: "Create or update a Kustomization reconciling a path from a " +
			"GitRepository source. Point --source at another source (or reuse the " +
			"same name) to repoint the base kustomization away from the platform " +
			"source.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := fluxcore.AddKustomization(cmd.Context(), fluxclient.New(*kubeconfig),
				fluxcore.KustomizationSpec{Name: args[0], Source: source, Path: path})
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(res)
		},
	}
	f := cmd.Flags()
	f.StringVar(&source, "source", fluxcore.DefaultSourceName, "GitRepository source name to reconcile from.")
	f.StringVar(&path, "path", fluxcore.DefaultSourcePath, "Path within the source to reconcile.")
	return cmd
}

func newKustomizationDeleteCmd(kubeconfig *string) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a Kustomization.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := fluxcore.RemoveKustomization(cmd.Context(), fluxclient.New(*kubeconfig), args[0]); err != nil {
				return render.Fail(err)
			}
			return render.JSON(map[string]string{"deleted_kustomization": args[0]})
		},
	}
}
