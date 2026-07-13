// Package flux holds the `flux` command group: compose GitOps sources and
// kustomizations on a cluster via the bundled flux CLI. The group is
// provider-agnostic (works on any kubeconfig, kind or DOKS); to target a DOKS
// cluster, fetch its kubeconfig first with `dctl do cluster connect`.
package flux

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	fluxclient "github.com/dantofa/platform/internal/clients/flux"
	"github.com/dantofa/platform/internal/clients/kube"
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
		Short: "Manage Flux sources (oci or git).",
	}
	source.AddCommand(newSourceCreateCmd(kubeconfig), newSourceDeleteCmd(kubeconfig))
	return source
}

// parseSourceType validates a --source-type flag against the two supported
// kinds. Shared by the source create/delete commands.
func parseSourceType(v string) (fluxcore.SourceType, error) {
	st := fluxcore.SourceType(v)
	if st != fluxcore.SourceOCI && st != fluxcore.SourceGit {
		return "", fmt.Errorf("--type must be %q or %q, got %q",
			fluxcore.SourceOCI, fluxcore.SourceGit, v)
	}
	return st, nil
}

func newSourceCreateCmd(kubeconfig *string) *cobra.Command {
	var url, sourceType, revision string
	var insecure bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create or update an OCIRepository or GitRepository source. Idempotent.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := parseSourceType(sourceType)
			if err != nil {
				return render.Fail(err)
			}
			if revision == "" {
				revision = st.DefaultRevision()
			}
			res, err := fluxcore.AddSource(cmd.Context(), fluxclient.New(*kubeconfig),
				fluxcore.SourceSpec{Type: st, Name: args[0], URL: url, Revision: revision, Insecure: insecure})
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(res)
		},
	}
	f := cmd.Flags()
	f.StringVar(&url, "url", "", "URL of the source (oci://... or a git URL).")
	_ = cmd.MarkFlagRequired("url")
	f.StringVar(&sourceType, "type", string(fluxcore.DefaultSourceType), `Source type: "oci" or "git".`)
	f.StringVar(&revision, "revision", "", `Revision to track: an OCI tag or git branch (default: "latest" for oci, "master" for git).`)
	f.BoolVar(&insecure, "insecure", false, "Allow a plain-HTTP OCI registry (the in-cluster kind registry).")
	return cmd
}

func newSourceDeleteCmd(kubeconfig *string) *cobra.Command {
	var sourceType string
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete an OCIRepository or GitRepository source.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := parseSourceType(sourceType)
			if err != nil {
				return render.Fail(err)
			}
			if err := fluxcore.RemoveSource(cmd.Context(), fluxclient.New(*kubeconfig), st, args[0]); err != nil {
				return render.Fail(err)
			}
			return render.JSON(map[string]string{"deleted_source": args[0]})
		},
	}
	cmd.Flags().StringVar(&sourceType, "type", string(fluxcore.DefaultSourceType), `Source type: "oci" or "git".`)
	return cmd
}

func newKustomizationCmd(kubeconfig *string) *cobra.Command {
	ks := &cobra.Command{
		Use:     "kustomization",
		Aliases: []string{"ks"},
		Short:   "Manage Flux Kustomizations.",
	}
	ks.AddCommand(
		newKustomizationCreateCmd(kubeconfig),
		newKustomizationDeleteCmd(kubeconfig),
		newKustomizationListCmd(kubeconfig),
		newKustomizationVerifyCmd(kubeconfig),
	)
	return ks
}

func newKustomizationListCmd(kubeconfig *string) *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Flux Kustomizations with their reconciliation status.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kc, err := kube.NewFromPath(*kubeconfig)
			if err != nil {
				return render.Fail(err)
			}
			statuses, err := fluxcore.ListKustomizations(cmd.Context(), kc, namespace)
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(statuses)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "",
		"Limit to one namespace (default: all namespaces).")
	return cmd
}

func newKustomizationVerifyCmd(kubeconfig *string) *cobra.Command {
	var (
		namespace string
		wait      bool
		timeout   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Gate on Flux Kustomizations: exit non-zero unless all are reconciled.",
		Long: "Print each Kustomization with its reconciliation status (same output " +
			"as `list`) and exit non-zero unless every one is reconciled — so it can " +
			"gate CI after a bootstrap/apply. --wait polls until they converge.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kc, err := kube.NewFromPath(*kubeconfig)
			if err != nil {
				return render.Fail(err)
			}
			var (
				statuses []fluxcore.KustomizationStatus
				ok       bool
			)
			if wait {
				statuses, ok, err = fluxcore.VerifyKustomizationsWait(cmd.Context(), kc, namespace, timeout, 5*time.Second)
			} else {
				statuses, ok, err = fluxcore.VerifyKustomizations(cmd.Context(), kc, namespace)
			}
			if err != nil {
				return render.Fail(err)
			}
			if err := render.JSON(statuses); err != nil {
				return err
			}
			// Gate: the list is already printed; a not-ready result is a non-zero
			// exit without re-printing.
			if !ok {
				return render.ErrHandled
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&namespace, "namespace", "n", "",
		"Limit to one namespace (default: all namespaces).")
	f.BoolVar(&wait, "wait", false, "Poll until every Kustomization is reconciled or --timeout elapses.")
	f.DurationVar(&timeout, "timeout", 5*time.Minute, "Maximum time to wait with --wait.")
	return cmd
}

func newKustomizationCreateCmd(kubeconfig *string) *cobra.Command {
	var source, path, sourceType string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create or update a Kustomization. Idempotent.",
		Long: "Create or update a Kustomization reconciling a path from a source. " +
			"--type selects the source kind (oci/git); point --source at another " +
			"source (or reuse the same name) to repoint the base kustomization away " +
			"from the platform source.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := parseSourceType(sourceType)
			if err != nil {
				return render.Fail(err)
			}
			res, err := fluxcore.AddKustomization(cmd.Context(), fluxclient.New(*kubeconfig),
				fluxcore.KustomizationSpec{Type: st, Name: args[0], Source: source, Path: path})
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(res)
		},
	}
	f := cmd.Flags()
	f.StringVar(&sourceType, "type", string(fluxcore.DefaultSourceType), `Source kind to reference: "oci" or "git".`)
	f.StringVar(&source, "source", fluxcore.DefaultSourceName, "Source name to reconcile from.")
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
