package digitalocean

import (
	"context"

	"github.com/spf13/cobra"

	doclient "github.com/dantofa/platform/internal/clients/digitalocean"
	docore "github.com/dantofa/platform/internal/core/digitalocean"
	"github.com/dantofa/platform/internal/render"
)

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
		Short: "Delete a bucket. Idempotent: succeeds if already absent (must be empty otherwise).",
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

	space.AddCommand(list, create, del, newSpaceLinkCmd(&region, &token), newSpaceUnlinkCmd(&region, &token))
	return space
}

func newSpaceUnlinkCmd(region, token *string) *cobra.Command {
	return &cobra.Command{
		Use:   "unlink <bucket>",
		Short: "Tear down a backup bucket: revoke its scoped key(s), empty and delete it.",
		Long: "Reverse of `link`: revoke the bucket-scoped Spaces key(s) dctl minted " +
			"for the bucket, empty the bucket (all object versions), and delete it. " +
			"Idempotent. Use in CI/preview teardown after a backup has run.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSpaces(cmd, *region, *token, func(ctx context.Context, client *doclient.SpacesClient) error {
				res, err := docore.Unlink(ctx, client, args[0])
				if err != nil {
					return err
				}
				return render.JSON(res)
			})
		},
	}
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
				return render.Fail(err)
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
	f.StringVar(&namespace, "namespace", "velero",
		"Namespace for the credential Secret and coordinates ConfigMap (where "+
			"Velero runs); created if absent.")
	f.StringVar(&secretName, "secret-name", "",
		"Credential Secret name (default "+doclient.DefaultSecretName+").")
	f.StringVar(&configMapName, "configmap-name", "",
		"Coordinates ConfigMap name (default "+doclient.DefaultConfigMapName+").")
	return cmd
}
