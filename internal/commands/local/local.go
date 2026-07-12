// Package local holds the `local` command group: kind development clusters
// wired to an internal OCI registry, over the local clients.
package local

import (
	"os"

	"github.com/spf13/cobra"

	fluxclient "github.com/dantofa/platform/internal/clients/flux"
	"github.com/dantofa/platform/internal/clients/kube"
	localclient "github.com/dantofa/platform/internal/clients/local"
	fluxcore "github.com/dantofa/platform/internal/core/flux"
	localcore "github.com/dantofa/platform/internal/core/local"
	"github.com/dantofa/platform/internal/render"
)

// NewCmd builds the `local` resource group. The Nix package bundles the
// runtime CLIs (kind/flux/docker) on PATH, so the group is always present; a
// missing tool surfaces a clear "not installed" error from the command.
func NewCmd() *cobra.Command {
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
		newLocalBootstrapCmd(),
		newLocalPushCmd(),
		newLocalDeleteCmd(),
		newLocalConnectCmd(),
	)
	return cluster
}

// writeTempKubeconfig writes kubeconfig bytes to a private temp file for the
// flux CLI, returning the path and a cleanup func.
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

// backupNamespace is where the local backup stack (SeaweedFS + Velero) runs. It
// matches the namespace the flux/local manifests target and the reused
// flux/cluster/velero stack declares.
const backupNamespace = "velero"

func newLocalBootstrapCmd() *cobra.Command {
	var (
		fluxVersion, registryName, artifactName, tag string
		sourceName, sourcePath                       string
	)
	cmd := &cobra.Command{
		Use:   "bootstrap [name]",
		Short: "Publish the local GitOps tree, install Flux, and wire it up.",
		Long: "Self-contained bring-up: publish the working-tree flux/ to the " +
			"in-cluster registry, install Flux, and point an OCIRepository + " +
			"Kustomization at ./flux/local so it reconciles immediately. Run once " +
			"after `create` (no separate `push` needed first); use `push` afterwards " +
			"to publish edits. The local tree stands up an in-cluster SeaweedFS " +
			"backup target so the Velero stack runs without a cloud provider.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := nameArg(args)
			kindClient := localclient.NewKindClient()

			// Publish the working-tree flux/ tree first, so the OCIRepository the
			// bootstrap registers can pull it immediately (no separate `push`).
			pushed, err := localcore.PushArtifact(ctx, kindClient, artifactName, tag,
				localcore.DefaultArtifactPath, localcore.DefaultRegistryPort)
			if err != nil {
				return render.Fail(err)
			}

			kubeconfig, err := localcore.GetKubeconfig(ctx, kindClient, name)
			if err != nil {
				return render.Fail(err)
			}
			kubePath, cleanup, err := writeTempKubeconfig([]byte(kubeconfig))
			if err != nil {
				return render.Fail(err)
			}
			defer cleanup()

			// Create the velero namespace imperatively before Flux reconciles, so
			// no local Flux stack has to declare it (the reused flux/cluster/velero
			// stack stays its sole owner) and the local backup manifests can be
			// applied into it. Mirrors the DOKS CLI's EnsureNamespace.
			kc, err := kube.NewFromKubeconfig([]byte(kubeconfig))
			if err != nil {
				return render.Fail(err)
			}
			if err := kc.EnsureNamespace(ctx, backupNamespace); err != nil {
				return render.Fail(err)
			}

			url, err := localcore.InClusterArtifactURL(ctx, kindClient, registryName, artifactName)
			if err != nil {
				return render.Fail(err)
			}
			res, err := fluxcore.BootstrapLocal(ctx, fluxclient.New(kubePath),
				fluxVersion, sourceName, url, tag, sourcePath)
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(map[string]string{
				"cluster":     name,
				"artifact":    pushed.Artifact,
				"flux_source": res.Source,
				"oci_url":     res.URL,
				"tag":         res.Tag,
				"flux_path":   res.Path,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&fluxVersion, "flux-version", "", "Flux version to install (default: the bundled flux CLI's version).")
	f.StringVar(&registryName, "registry-name", localcore.DefaultRegistryName, "In-cluster OCI registry name the OCIRepository pulls from.")
	f.StringVar(&artifactName, "artifact-name", localcore.DefaultArtifactName, "OCI artifact name (matches `push`).")
	f.StringVarP(&tag, "tag", "t", localcore.DefaultArtifactTag, "OCI tag to track.")
	f.StringVar(&sourceName, "source-name", fluxcore.DefaultSourceName, "Name of the Flux OCIRepository and Kustomization.")
	f.StringVar(&sourcePath, "source-path", fluxcore.DefaultLocalSourcePath, "Path within the artifact that Flux reconciles.")
	return cmd
}

func newLocalListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List local clusters.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			clusters, err := localcore.ListClusters(cmd.Context(), localclient.NewKindClient())
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(clusters)
		},
	}
}

func newLocalCreateCmd() *cobra.Command {
	var (
		registryName string
		registryPort int
		workers      int
		verbose      bool
	)
	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a kind cluster wired to an internal OCI registry.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := localclient.NewKindClient(localclient.WithProgress(verbose))
			result, err := localcore.CreateCluster(cmd.Context(), client,
				nameArg(args), registryName, registryPort, workers)
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(result)
		},
	}
	f := cmd.Flags()
	f.StringVar(&registryName, "registry-name", localcore.DefaultRegistryName,
		"Name of the internal OCI registry container.")
	f.IntVar(&registryPort, "registry-port", localcore.DefaultRegistryPort,
		"Host port the registry is pushable on.")
	f.IntVar(&workers, "workers", localcore.DefaultWorkerNodes,
		"Number of worker nodes (0 for a single-node control-plane).")
	f.BoolVar(&verbose, "verbose", false,
		"Stream kind's provisioning progress to stderr as it runs.")
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
				return render.Fail(err)
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
				return render.Fail(err)
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
				return render.Fail(err)
			}
			if err := render.WriteOwnerOnly(output, kubeconfig); err != nil {
				return render.Fail(err)
			}
			return render.JSON(map[string]string{"name": name, "kubeconfig": output})
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", ".kubeconfig", "Where to write the kubeconfig.")
	return cmd
}
