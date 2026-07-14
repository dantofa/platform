package digitalocean

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	doclient "github.com/dantofa/platform/internal/clients/digitalocean"
	fluxclient "github.com/dantofa/platform/internal/clients/flux"
	"github.com/dantofa/platform/internal/clients/kube"
	docore "github.com/dantofa/platform/internal/core/digitalocean"
	fluxcore "github.com/dantofa/platform/internal/core/flux"
	"github.com/dantofa/platform/internal/render"
)

func newClusterCmd() *cobra.Command {
	var token string
	cluster := &cobra.Command{
		Use:          "cluster",
		Short:        "Manage DigitalOcean Kubernetes (DOKS) clusters.",
		SilenceUsage: true,
	}
	cluster.PersistentFlags().StringVar(&token, "token", "",
		"DigitalOcean API token (defaults to $DIGITALOCEAN_ACCESS_TOKEN).")

	cluster.AddCommand(
		newClusterListCmd(&token),
		newClusterCreateCmd(&token),
		newClusterUpdateCmd(&token),
		newClusterConnectCmd(&token),
		newClusterDeleteCmd(&token),
		newClusterBootstrapCmd(&token),
	)
	return cluster
}

func newClusterListCmd(token *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all clusters.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := doclient.NewClusterClient(*token)
			if err != nil {
				return render.Fail(err)
			}
			clusters, err := docore.ListClusters(cmd.Context(), client)
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(clusters)
		},
	}
}

func newClusterCreateCmd(token *string) *cobra.Command {
	var (
		name, region, version, poolSize string
		poolCount, poolMin, poolMax     int
		ha, wait                        bool
		tags                            []string
		waitTimeout                     float64
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a DOKS cluster.",
		Long: "Create a DOKS cluster. The node pool is always named \"system\" with " +
			"autoscaling enabled, and auto-upgrade and surge-upgrade are always on. " +
			"Only HA, node sizing and tags are configurable.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := doclient.NewClusterClient(*token)
			if err != nil {
				return render.Fail(err)
			}
			pool := docore.BuildNodePool("system", poolSize, poolCount, poolMin, poolMax)
			spec := docore.BuildCreateSpec(name, region, version, pool, tags, ha)
			result, err := docore.CreateCluster(cmd.Context(), client, spec)
			if err != nil {
				return render.Fail(err)
			}
			if wait {
				result, err = docore.WaitForRunning(cmd.Context(), client, name,
					time.Duration(waitTimeout*float64(time.Second)), docore.DefaultPollInterval)
				if err != nil {
					return render.Fail(err)
				}
			}
			return render.JSON(result)
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "Cluster name.")
	_ = cmd.MarkFlagRequired("name")
	f.StringVar(&region, "region", "nyc3", "Region slug, e.g. nyc3.")
	f.StringVar(&version, "version", "latest", `Kubernetes version slug, or "latest".`)
	f.StringVar(&poolSize, "node-pool-size", "s-2vcpu-4gb", "Primary node pool droplet size slug.")
	f.IntVar(&poolCount, "node-pool-count", 2, "Initial node count.")
	f.IntVar(&poolMin, "node-pool-min", 2, "Minimum nodes (autoscaling).")
	f.IntVar(&poolMax, "node-pool-max", 10, "Maximum nodes (autoscaling).")
	f.BoolVar(&ha, "ha", false, "Enable HA control plane.")
	f.StringArrayVar(&tags, "tag", nil, "A cluster tag; repeatable.")
	f.BoolVar(&wait, "wait", false, "Wait until the cluster reaches the running state.")
	f.Float64Var(&waitTimeout, "wait-timeout", 900, "Seconds to wait for running (with --wait).")
	return cmd
}

func newClusterUpdateCmd(token *string) *cobra.Command {
	var (
		ha, clearTags bool
		tags          []string
	)
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update a cluster's mutable fields (HA, tags).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if clearTags && len(tags) > 0 {
				return fmt.Errorf("--clear-tags and --tag are mutually exclusive")
			}
			// No tag flags = leave tags untouched; --clear-tags = replace with [];
			// --tag = replace with the given tags.
			var tagsPtr *[]string
			switch {
			case clearTags:
				empty := []string{}
				tagsPtr = &empty
			case len(tags) > 0:
				tagsPtr = &tags
			}
			client, err := doclient.NewClusterClient(*token)
			if err != nil {
				return render.Fail(err)
			}
			spec := docore.BuildUpdateSpec(tagsPtr, ha)
			result, err := docore.UpdateCluster(cmd.Context(), client, args[0], spec)
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(result)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&ha, "ha", false, "Enable HA control plane.")
	f.StringArrayVar(&tags, "tag", nil, "Replace the cluster tags; repeatable.")
	f.BoolVar(&clearTags, "clear-tags", false, "Remove all tags from the cluster.")
	return cmd
}

func newClusterConnectCmd(token *string) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "connect <name>",
		Short: "Fetch a cluster's kubeconfig and write it to a local file.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := doclient.NewClusterClient(*token)
			if err != nil {
				return render.Fail(err)
			}
			kubeconfig, err := docore.GetKubeconfig(cmd.Context(), client, args[0])
			if err != nil {
				return render.Fail(err)
			}
			if err := render.WriteOwnerOnly(output, kubeconfig); err != nil {
				return render.Fail(err)
			}
			return render.JSON(map[string]string{"name": args[0], "kubeconfig": output})
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", ".kubeconfig", "Where to write the kubeconfig.")
	return cmd
}

func newClusterDeleteCmd(token *string) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a cluster by name. Idempotent: succeeds if already absent.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := doclient.NewClusterClient(*token)
			if err != nil {
				return render.Fail(err)
			}
			if _, err := docore.DeleteCluster(cmd.Context(), client, args[0]); err != nil {
				return render.Fail(err)
			}
			return nil
		},
	}
}

func newClusterBootstrapCmd(token *string) *cobra.Command {
	var (
		bucket, region, fluxVersion           string
		sourceType, sourceURL, sourceRevision string
		sourcePath, src, baseDomain           string
		namespace, secretName, configMapName  string
	)
	cmd := &cobra.Command{
		Use:   "bootstrap <cluster>",
		Short: "Bootstrap a cluster for GitOps backups.",
		Long: "Link a versioned Spaces backup bucket + scoped credential into the " +
			"cluster, install Flux, and point it at the platform source. The DO " +
			"token stays with you and never enters the cluster; only the " +
			"bucket-scoped key is stored there. Idempotent.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cluster := args[0]
			ctx := cmd.Context()
			if bucket == "" {
				bucket = cluster + "-backup"
			}

			// Fetch the cluster's kubeconfig via the DO token (to a temp file the
			// flux CLI can consume).
			cc, err := doclient.NewClusterClient(*token)
			if err != nil {
				return render.Fail(err)
			}
			kubeconfig, err := docore.GetKubeconfig(ctx, cc, cluster)
			if err != nil {
				return render.Fail(err)
			}
			kubePath, cleanup, err := writeTempKubeconfig([]byte(kubeconfig))
			if err != nil {
				return render.Fail(err)
			}
			defer cleanup()
			kc, err := kube.NewFromPath(kubePath)
			if err != nil {
				return render.Fail(err)
			}

			// 1. Link the backup bucket + credential into the cluster.
			if err := withSpaces(cmd, region, *token, func(ctx context.Context, sc *doclient.SpacesClient) error {
				store := doclient.NewCredentialStore(kc, namespace, secretName, configMapName)
				_, err := docore.LinkAndStore(ctx, sc, store, bucket)
				return err
			}); err != nil {
				return err // withSpaces already rendered
			}

			// 2. Install Flux, register the platform source (oci by default, git for
			// downstream), and apply the shared `cluster` reconcile root. The root
			// propagates the source into the source-agnostic ./flux/cluster stacks.
			st := fluxcore.SourceType(sourceType)
			if st != fluxcore.SourceOCI && st != fluxcore.SourceGit {
				return render.Fail(fmt.Errorf("--source-type must be %q or %q, got %q",
					fluxcore.SourceOCI, fluxcore.SourceGit, sourceType))
			}
			if sourceRevision == "" {
				sourceRevision = st.DefaultRevision()
			}
			if sourceURL == "" {
				sourceURL = fluxcore.DefaultSourceURL
				if st == fluxcore.SourceOCI {
					sourceURL = fluxcore.DefaultOCISourceURL
				}
			}
			vars := map[string]string{
				fluxcore.VarBaseDomain:  baseDomain,
				fluxcore.VarClusterName: cluster,
			}
			res, err := fluxcore.Bootstrap(ctx, fluxclient.New(kubePath), kc, fluxVersion,
				fluxcore.SourceSpec{Type: st, Name: src, URL: sourceURL, Revision: sourceRevision},
				vars,
				[]fluxcore.ReconcileRoot{{
					Name: fluxcore.ClusterRootName, Path: sourcePath, Substitute: true,
				}})
			if err != nil {
				return render.Fail(err)
			}

			return render.JSON(map[string]any{
				"cluster":        cluster,
				"bucket":         bucket,
				"flux_source":    res.Source,
				"source_kind":    res.SourceKind,
				"revision":       res.Revision,
				"kustomizations": res.Kustomizations,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&bucket, "bucket", "", "Backup bucket name (default <cluster>-backup).")
	f.StringVar(&region, "region", "", "Spaces region (defaults to $DIGITALOCEAN_SPACES_REGION / nyc3).")
	f.StringVar(&fluxVersion, "flux-version", "", "Flux version to install (default: the bundled flux CLI's version).")
	f.StringVar(&sourceType, "source-type", string(fluxcore.DefaultSourceType), `GitOps source type: "oci" or "git".`)
	f.StringVar(&sourceURL, "source-url", "", "URL of the GitOps source (default: the OCI/git URL for --source-type).")
	f.StringVar(&sourceRevision, "source-revision", "", `Source revision to track (default: "latest" for oci, "master" for git).`)
	f.StringVar(&sourcePath, "source-path", fluxcore.DefaultSourcePath, "Path within the source that Flux reconciles.")
	f.StringVar(&src, "source-name", fluxcore.DefaultSourceName, "Name of the Flux source and reconcile root.")
	f.StringVar(&baseDomain, "base-domain", "", "Cluster ingress FQDN (${base_domain} in cluster-vars). Required.")
	_ = cmd.MarkFlagRequired("base-domain")
	f.StringVar(&namespace, "namespace", "velero", "Namespace for the credential Secret and coordinates ConfigMap (where Velero runs); created if absent.")
	f.StringVar(&secretName, "secret-name", "", "Credential Secret name (default "+doclient.DefaultSecretName+").")
	f.StringVar(&configMapName, "configmap-name", "", "Coordinates ConfigMap name (default "+doclient.DefaultConfigMapName+").")
	return cmd
}
