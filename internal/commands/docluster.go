package commands

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	doclient "github.com/dantofa/platform/internal/clients/digitalocean"
	docore "github.com/dantofa/platform/internal/core/digitalocean"
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
				render.Error(err)
				return errHandled
			}
			clusters, err := docore.ListClusters(cmd.Context(), client)
			if err != nil {
				render.Error(err)
				return errHandled
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
				render.Error(err)
				return errHandled
			}
			pool := docore.BuildNodePool("system", poolSize, poolCount, poolMin, poolMax)
			spec := docore.BuildCreateSpec(name, region, version, pool, tags, ha)
			result, err := docore.CreateCluster(cmd.Context(), client, spec)
			if err != nil {
				render.Error(err)
				return errHandled
			}
			if wait {
				result, err = docore.WaitForRunning(cmd.Context(), client, name,
					time.Duration(waitTimeout*float64(time.Second)), docore.DefaultPollInterval)
				if err != nil {
					render.Error(err)
					return errHandled
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
				render.Error(err)
				return errHandled
			}
			spec := docore.BuildUpdateSpec(tagsPtr, ha)
			result, err := docore.UpdateCluster(cmd.Context(), client, args[0], spec)
			if err != nil {
				render.Error(err)
				return errHandled
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
				render.Error(err)
				return errHandled
			}
			kubeconfig, err := docore.GetKubeconfig(cmd.Context(), client, args[0])
			if err != nil {
				render.Error(err)
				return errHandled
			}
			if err := render.WriteOwnerOnly(output, kubeconfig); err != nil {
				render.Error(err)
				return errHandled
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
				render.Error(err)
				return errHandled
			}
			if _, err := docore.DeleteCluster(cmd.Context(), client, args[0]); err != nil {
				render.Error(err)
				return errHandled
			}
			return nil
		},
	}
}
