package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	cfclient "github.com/dantofa/platform/internal/clients/cloudflare"
	doclient "github.com/dantofa/platform/internal/clients/digitalocean"
	fluxclient "github.com/dantofa/platform/internal/clients/flux"
	"github.com/dantofa/platform/internal/clients/kube"
	docore "github.com/dantofa/platform/internal/core/digitalocean"
	fluxcore "github.com/dantofa/platform/internal/core/flux"
	teardowncore "github.com/dantofa/platform/internal/core/teardown"
	"github.com/dantofa/platform/internal/render"
)

// Teardown drain bounds: how long to wait for the ingress controller to remove
// the Cloudflare records after the Ingresses are deleted, and the poll cadence.
const (
	teardownTimeout  = 3 * time.Minute
	teardownInterval = 5 * time.Second
)

// newDNSClient adapts the cloudflare client constructor to the teardown factory
// signature (concrete -> interface).
func newDNSClient(token string) (teardowncore.DNSAPI, error) {
	return cfclient.New(token)
}

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
	var force, noTeardown bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a cluster by name. Idempotent: succeeds if already absent.",
		Long: "Delete a cluster by name. First gracefully tears down its ingress " +
			"(deletes the Ingress objects so external-dns / the tunnel controller " +
			"remove the Cloudflare records, then waits for the records to clear) so " +
			"the destroyed cluster does not orphan DNS records. --no-teardown skips " +
			"this; --force deletes anyway if teardown fails.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cluster := args[0]
			ctx := cmd.Context()
			client, err := doclient.NewClusterClient(*token)
			if err != nil {
				return render.Fail(err)
			}
			if !noTeardown {
				if err := teardownCluster(ctx, cluster, *token, force); err != nil {
					return err // already rendered
				}
			}
			if _, err := docore.DeleteCluster(ctx, client, cluster); err != nil {
				return render.Fail(err)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&force, "force", false, "Delete even if graceful ingress teardown fails (may orphan DNS records).")
	f.BoolVar(&noTeardown, "no-teardown", false, "Skip graceful ingress teardown and destroy immediately.")
	return cmd
}

// teardownCluster runs the graceful ingress/DNS drain before a DOKS cluster is
// destroyed. A cluster that no longer exists is a no-op (delete is idempotent).
// On a teardown failure it renders the error and returns it, unless force is set
// (then it warns and lets the delete proceed).
func teardownCluster(ctx context.Context, cluster, token string, force bool) error {
	kc, err := kubeClient(ctx, cluster, "", token)
	if err != nil {
		var notFound *docore.ClusterNotFoundError
		if errors.As(err, &notFound) {
			return nil // nothing to tear down
		}
		return teardownFailed(fmt.Errorf("connecting to cluster for teardown: %w", err), force)
	}
	res, err := teardowncore.Run(ctx, kc, kc, newDNSClient, teardownTimeout, teardownInterval)
	if err != nil {
		return teardownFailed(err, force)
	}
	return render.JSON(map[string]any{"teardown": res})
}

// teardownFailed renders a teardown error and returns it to abort the delete —
// unless force is set, in which case it warns and returns nil so delete proceeds.
func teardownFailed(err error, force bool) error {
	if force {
		fmt.Fprintln(os.Stderr, "warning: ingress teardown failed, deleting anyway (--force):", err)
		return nil
	}
	render.Error(err)
	return render.ErrHandled
}

func newClusterBootstrapCmd(token *string) *cobra.Command {
	var (
		bucket, region, fluxVersion           string
		sourceType, sourceURL, sourceRevision string
		sourcePath, src, baseDomain           string
		bwToken, bwProjectID, bwOrgID         string
		namespace, secretName, configMapName  string
		tlsIssuer                             string
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

			// 2. Plant the ESO secret-zero (Bitwarden token); project/org scope the
			// ClusterSecretStore via cluster-vars below. All default from the bws env.
			if bwToken == "" {
				bwToken = os.Getenv("BWS_ACCESS_TOKEN")
			}
			if bwProjectID == "" {
				bwProjectID = os.Getenv("BWS_PROJECT_ID")
			}
			if bwOrgID == "" {
				bwOrgID = os.Getenv("BWS_ORGANIZATION_ID")
			}
			if err := fluxcore.ValidateBitwardenConfig(bwToken, bwProjectID, bwOrgID); err != nil {
				return render.Fail(err)
			}
			if err := fluxcore.ValidateTLSIssuer(tlsIssuer); err != nil {
				return render.Fail(err)
			}
			if err := fluxcore.ProvisionESOAccessToken(ctx, kc, bwToken); err != nil {
				return render.Fail(err)
			}

			// 3. Install Flux, register the platform source (oci by default, git for
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
			dnsZone, err := fluxcore.DNSZone(baseDomain)
			if err != nil {
				return render.Fail(err)
			}
			vars := map[string]string{
				fluxcore.VarBaseDomain:         baseDomain,
				fluxcore.VarClusterName:        cluster,
				fluxcore.VarBitwardenOrgID:     bwOrgID,
				fluxcore.VarBitwardenProjectID: bwProjectID,
				fluxcore.VarTLSIssuer:          tlsIssuer,
				fluxcore.VarDNSZone:            dnsZone,
			}
			roots := []fluxcore.ReconcileRoot{
				{Name: fluxcore.ClusterRootName, Path: sourcePath, Substitute: true},
				// Traefik (ingress) and external-dns (DNS) are separate stacks.
				// Traefik's default cert is issued by cert-manager (Certificate in
				// certificate.yaml, ${tls_issuer} ClusterIssuer), so it waits on
				// cert-manager-config (Certificate CRD + the selfsigned issuer);
				// external-dns pulls its Cloudflare token from bws, so it waits on
				// eso-config.
				{
					Name:       fluxcore.IngressRootName,
					Path:       fluxcore.DefaultRemoteIngressPath,
					DependsOn:  []string{fluxcore.CertManagerConfigName},
					Substitute: true,
				},
				{
					Name:       fluxcore.ExternalDNSRootName,
					Path:       fluxcore.DefaultExternalDNSPath,
					DependsOn:  []string{fluxcore.ESOConfigName},
					Substitute: true,
				},
			}
			// The letsencrypt ACME issuer (+ its Cloudflare DNS-01 token) is only
			// needed when the Traefik cert is issued by it. It needs the
			// ClusterIssuer CRD (cert-manager) and the bitwarden store for the
			// token ExternalSecret (eso-config); the Traefik Certificate resolves
			// against it asynchronously once it is Ready.
			if tlsIssuer == fluxcore.TLSIssuerLetsEncrypt {
				roots = append(roots, fluxcore.ReconcileRoot{
					Name:      fluxcore.LetsEncryptRootName,
					Path:      fluxcore.DefaultLetsEncryptPath,
					DependsOn: []string{fluxcore.CertManagerConfigName, fluxcore.ESOConfigName},
				})
			}
			res, err := fluxcore.Bootstrap(ctx, fluxclient.New(kubePath), kc, fluxVersion,
				fluxcore.SourceSpec{Type: st, Name: src, URL: sourceURL, Revision: sourceRevision},
				vars, roots)
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
	f.StringVar(&tlsIssuer, "tls-issuer", fluxcore.TLSIssuerSelfSigned,
		`cert-manager ClusterIssuer for the Traefik default cert: "selfsigned" (Cloudflare Full) or "letsencrypt" (Full strict, DNS-01).`)
	f.StringVar(&bwToken, "bitwarden-token", "", "Bitwarden machine-account token for the ESO secret-zero (default $BWS_ACCESS_TOKEN).")
	f.StringVar(&bwProjectID, "bitwarden-project-id", "", "Bitwarden project ID for the ClusterSecretStore (default $BWS_PROJECT_ID).")
	f.StringVar(&bwOrgID, "bitwarden-org-id", "", "Bitwarden organization ID for the ClusterSecretStore (default $BWS_ORGANIZATION_ID).")
	f.StringVar(&namespace, "namespace", "velero", "Namespace for the credential Secret and coordinates ConfigMap (where Velero runs); created if absent.")
	f.StringVar(&secretName, "secret-name", "", "Credential Secret name (default "+doclient.DefaultSecretName+").")
	f.StringVar(&configMapName, "configmap-name", "", "Coordinates ConfigMap name (default "+doclient.DefaultConfigMapName+").")
	return cmd
}
