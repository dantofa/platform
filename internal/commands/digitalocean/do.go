// Package digitalocean holds the `do` command group (aliased `digitalocean`):
// cluster and space subcommands over the DigitalOcean clients.
package digitalocean

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	doclient "github.com/dantofa/platform/internal/clients/digitalocean"
	"github.com/dantofa/platform/internal/clients/kube"
	docore "github.com/dantofa/platform/internal/core/digitalocean"
	"github.com/dantofa/platform/internal/render"
)

// NewCmd builds the `do` resource group (aliased `digitalocean` for shells/CI
// where `do` is a reserved word; the alias is not listed separately).
func NewCmd() *cobra.Command {
	do := &cobra.Command{
		Use:          "do",
		Aliases:      []string{"digitalocean"},
		Short:        "Manage DigitalOcean resources.",
		SilenceUsage: true,
	}
	do.AddCommand(newClusterCmd())
	do.AddCommand(newSpaceCmd())
	return do
}

// withSpaces builds a Spaces client (minting an ephemeral credential when no
// standing keys are set), runs fn, and always revokes any ephemeral key — so it
// is removed even when the operation fails.
func withSpaces(cmd *cobra.Command, region, token string, fn func(ctx context.Context, client *doclient.SpacesClient) error) error {
	ctx := cmd.Context()
	client, err := doclient.NewSpacesClient(ctx, region, token)
	if err != nil {
		return render.Fail(err)
	}
	defer func() {
		if cerr := client.Close(); cerr != nil {
			fmt.Fprintln(os.Stderr, "warning: failed to revoke ephemeral Spaces key:", cerr)
		}
	}()
	if err := fn(ctx, client); err != nil {
		return render.Fail(err)
	}
	return nil
}

// kubeClient resolves a Kubernetes client either by fetching the kubeconfig for
// a named DOKS cluster (via the DO token) or from a kubeconfig path / the
// default loading rules.
func kubeClient(ctx context.Context, cluster, kubeconfigPath, token string) (*kube.Client, error) {
	if cluster != "" {
		cc, err := doclient.NewClusterClient(token)
		if err != nil {
			return nil, err
		}
		kubeconfig, err := docore.GetKubeconfig(ctx, cc, cluster)
		if err != nil {
			return nil, err
		}
		return kube.NewFromKubeconfig([]byte(kubeconfig))
	}
	return kube.NewFromPath(kubeconfigPath)
}

// writeTempKubeconfig writes kubeconfig bytes to a private temp file for tools
// (the flux CLI) that need a path, returning the path and a cleanup func.
func writeTempKubeconfig(data []byte) (string, func(), error) {
	f, err := os.CreateTemp("", "dctl-kubeconfig-*.yaml")
	if err != nil {
		return "", nil, err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	_ = f.Close()
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}
