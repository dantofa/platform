package commands

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

// newDOCmd builds the `do` resource group (aliased `digitalocean` for shells/CI
// where `do` is a reserved word; the alias is not listed separately).
func newDOCmd() *cobra.Command {
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
		render.Error(err)
		return errHandled
	}
	defer func() {
		if cerr := client.Close(); cerr != nil {
			fmt.Fprintln(os.Stderr, "warning: failed to revoke ephemeral Spaces key:", cerr)
		}
	}()
	if err := fn(ctx, client); err != nil {
		render.Error(err)
		return errHandled
	}
	return nil
}

func newSpaceCmd() *cobra.Command {
	var region, token string
	space := &cobra.Command{
		Use:   "space",
		Short: "Manage DigitalOcean Spaces buckets.",
	}
	pf := space.PersistentFlags()
	pf.StringVar(&region, "region", "",
		"Spaces region slug, e.g. nyc3 (defaults to $DIGITALOCEAN_SPACES_REGION).")
	pf.StringVar(&token, "token", "",
		"DigitalOcean API token for minting an ephemeral Spaces key when no "+
			"standing keys are set (defaults to $DIGITALOCEAN_ACCESS_TOKEN).")

	list := &cobra.Command{
		Use:   "list",
		Short: "List all Spaces buckets.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withSpaces(cmd, region, token, func(ctx context.Context, client *doclient.SpacesClient) error {
				buckets, err := docore.ListBuckets(ctx, client)
				if err != nil {
					return err
				}
				return render.JSON(buckets)
			})
		},
	}

	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a bucket.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSpaces(cmd, region, token, func(ctx context.Context, client *doclient.SpacesClient) error {
				if err := docore.CreateBucket(ctx, client, args[0]); err != nil {
					return err
				}
				return render.JSON(map[string]string{"created": args[0]})
			})
		},
	}

	del := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a bucket (must be empty).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSpaces(cmd, region, token, func(ctx context.Context, client *doclient.SpacesClient) error {
				if err := docore.DeleteBucket(ctx, client, args[0]); err != nil {
					return err
				}
				return render.JSON(map[string]string{"deleted": args[0]})
			})
		},
	}

	space.AddCommand(list, create, del, newSpaceLinkCmd(&region, &token))
	return space
}

func newSpaceLinkCmd(region, token *string) *cobra.Command {
	var cluster, kubeconfigPath, namespace, secretName, configMapName string
	cmd := &cobra.Command{
		Use:   "link <bucket>",
		Short: "Link a Spaces bucket to a cluster for backups.",
		Long: "Ensure a versioned Spaces bucket exists, mint a bucket-scoped " +
			"read/write/delete credential, and store it in the cluster (a Secret " +
			"plus a coordinates ConfigMap). Idempotent: re-run to rotate the " +
			"credential. The DO token stays with the caller and never enters the " +
			"cluster; only the bucket-scoped key is stored there.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bucket := args[0]
			kc, err := kubeClient(cmd.Context(), cluster, kubeconfigPath, *token)
			if err != nil {
				render.Error(err)
				return errHandled
			}
			return withSpaces(cmd, *region, *token, func(ctx context.Context, client *doclient.SpacesClient) error {
				store := doclient.NewCredentialStore(kc, namespace, secretName, configMapName)
				res, err := docore.LinkAndStore(ctx, client, store, bucket)
				if err != nil {
					return err
				}
				return render.JSON(map[string]string{
					"bucket":     res.Coordinates.Bucket,
					"region":     res.Coordinates.Region,
					"endpoint":   res.Coordinates.Endpoint,
					"namespace":  namespace,
					"secret":     store.SecretName(),
					"config_map": store.ConfigMapName(),
				})
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&cluster, "cluster", "",
		"DOKS cluster name whose kubeconfig to fetch via the DO token.")
	f.StringVar(&kubeconfigPath, "kubeconfig", "",
		"Kubeconfig path when --cluster is unset (defaults to $KUBECONFIG / ~/.kube/config).")
	f.StringVar(&namespace, "namespace", "flux-system",
		"Namespace for the credential Secret and coordinates ConfigMap.")
	f.StringVar(&secretName, "secret-name", "",
		"Credential Secret name (default "+doclient.DefaultSecretName+").")
	f.StringVar(&configMapName, "configmap-name", "",
		"Coordinates ConfigMap name (default "+doclient.DefaultConfigMapName+").")
	return cmd
}
