package commands

import (
	"github.com/spf13/cobra"

	localclient "github.com/dantofa/platform/internal/clients/local"
	localcore "github.com/dantofa/platform/internal/core/local"
	"github.com/dantofa/platform/internal/render"
)

// newLocalCmd builds the `local` resource group (registered only when kind is
// on PATH — see NewRootCmd).
func newLocalCmd() *cobra.Command {
	local := &cobra.Command{
		Use:          "local",
		Short:        "Manage local (kind) development clusters.",
		SilenceUsage: true,
	}
	local.AddCommand(newLocalClusterCmd())
	return local
}

func nameArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return localcore.DefaultClusterName
}

func newLocalClusterCmd() *cobra.Command {
	cluster := &cobra.Command{
		Use:          "cluster",
		Short:        "Manage local (kind) development clusters.",
		SilenceUsage: true,
	}
	cluster.AddCommand(
		newLocalListCmd(),
		newLocalCreateCmd(),
		newLocalPushCmd(),
		newLocalDeleteCmd(),
		newLocalConnectCmd(),
	)
	return cluster
}

func newLocalListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List local clusters.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			clusters, err := localcore.ListClusters(cmd.Context(), localclient.NewKindClient())
			if err != nil {
				render.Error(err)
				return errHandled
			}
			return render.JSON(clusters)
		},
	}
}

func newLocalCreateCmd() *cobra.Command {
	var (
		registryName string
		registryPort int
	)
	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a kind cluster wired to an internal OCI registry.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := localcore.CreateCluster(cmd.Context(), localclient.NewKindClient(),
				nameArg(args), registryName, registryPort)
			if err != nil {
				render.Error(err)
				return errHandled
			}
			return render.JSON(result)
		},
	}
	f := cmd.Flags()
	f.StringVar(&registryName, "registry-name", localcore.DefaultRegistryName,
		"Name of the internal OCI registry container.")
	f.IntVar(&registryPort, "registry-port", localcore.DefaultRegistryPort,
		"Host port the registry is pushable on.")
	return cmd
}

func newLocalPushCmd() *cobra.Command {
	var (
		path, name, tag string
		registryPort    int
	)
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Publish the project as an OCI artifact and reconcile Flux.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := localcore.PushArtifact(cmd.Context(), localclient.NewKindClient(),
				name, tag, path, registryPort)
			if err != nil {
				render.Error(err)
				return errHandled
			}
			return render.JSON(result)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&path, "path", "p", localcore.DefaultArtifactPath, "Directory to package as the OCI artifact.")
	f.StringVar(&name, "name", localcore.DefaultArtifactName, "OCI repository name (matches the OCIRepository).")
	f.StringVarP(&tag, "tag", "t", localcore.DefaultArtifactTag, "OCI tag.")
	f.IntVar(&registryPort, "registry-port", localcore.DefaultRegistryPort, "Host port of the local registry.")
	return cmd
}

func newLocalDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete [name]",
		Short: "Delete a local cluster. Idempotent: succeeds if already absent.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := localcore.DeleteCluster(cmd.Context(), localclient.NewKindClient(), nameArg(args)); err != nil {
				render.Error(err)
				return errHandled
			}
			return nil
		},
	}
}

func newLocalConnectCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "connect [name]",
		Short: "Write a local cluster's kubeconfig to a file.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := nameArg(args)
			kubeconfig, err := localcore.GetKubeconfig(cmd.Context(), localclient.NewKindClient(), name)
			if err != nil {
				render.Error(err)
				return errHandled
			}
			if err := render.WriteOwnerOnly(output, kubeconfig); err != nil {
				render.Error(err)
				return errHandled
			}
			return render.JSON(map[string]string{"name": name, "kubeconfig": output})
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", ".kubeconfig", "Where to write the kubeconfig.")
	return cmd
}
