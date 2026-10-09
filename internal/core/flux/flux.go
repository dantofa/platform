// Package flux holds the framework-free application logic for composing Flux
// GitOps on a cluster: installing Flux and registering/removing GitRepository
// sources and Kustomizations. The flux CLI surface is reached through the
// Engine interface, satisfied by the clients adapter, so this package imports
// neither cobra nor a client SDK and is reused by the future operator.
package flux

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/dantofa/platform/internal/core/retry"
)

// Defaults for the platform GitOps source a cluster is bootstrapped against.
// The composable commands and `do cluster bootstrap` share these so the base
// source/kustomization stay consistent.
const (
	DefaultSourceName   = "platform"
	DefaultSourceURL    = "https://github.com/dantofa/platform"
	DefaultSourceBranch = "master"
	// DefaultOCISourceURL is the published OCI artifact a remote cluster pulls
	// when bootstrapped with --source-type oci (the default). Local clusters
	// override this with the in-cluster kind registry URL.
	DefaultOCISourceURL = "oci://ghcr.io/dantofa/platform"
	// DefaultOCIRevision is the OCI tag a source tracks by default (mutable:
	// re-pulled every reconcile interval). DefaultSourceBranch is the git
	// equivalent.
	DefaultOCIRevision = "latest"
	// DefaultSourcePath is the shared, source-agnostic reconcile root every
	// cluster loads (Velero + Kyverno). Its nested Kustomizations reference the
	// source via ${source_kind}/${source_name}, filled in by the reconcile
	// root's postBuild.substituteFrom the cluster-vars ConfigMap.
	DefaultSourcePath = "./flux/cluster"
	// DefaultLocalSourcePath is the local/kind-only requirements root: the
	// SeaweedFS backend that stands in for a cloud bucket plus the backup
	// contract. It is only ever reconciled from OCI, so it hardcodes its source.
	DefaultLocalSourcePath = "./flux/local"

	// ClusterRootName is the reconcile root that loads the shared ./flux/cluster
	// stacks on every cluster type. LocalRequirementsRootName loads the
	// local-only ./flux/local requirements ahead of it on kind clusters.
	// IngressRootName loads a per-cluster-type ingress layer after the cluster
	// root (so ESO exists), e.g. ./flux/ingress/tunnel on kind.
	ClusterRootName           = "cluster"
	LocalRequirementsRootName = "local-requirements"
	IngressRootName           = "ingress"
	// DefaultTunnelIngressPath is the Cloudflare Tunnel ingress layer (outbound,
	// no LoadBalancer): the DOKS default unless --dolb is given.
	// DefaultRemoteIngressPath is the DOKS --dolb layer: Traefik + external-dns
	// behind a DO LoadBalancer, proxied by Cloudflare. DefaultLocalIngressPath is the
	// kind default: Traefik on a NodePort Service that kind publishes to the host's
	// real :80/:443, so the base domain's loopback DNS record reaches it directly (no
	// Cloudflare/tunnel), keeping local test traffic on the loopback.
	// All set their controller as the default IngressClass, so the same vanilla
	// Ingress objects route on any cluster type.
	DefaultTunnelIngressPath = "./flux/ingress/tunnel"
	DefaultRemoteIngressPath = "./flux/ingress/traefik"
	DefaultLocalIngressPath  = "./flux/local/traefik"
	// LocalCARootName / DefaultLocalCAPath is the kind-only local CA layer: a
	// self-signed root minted by cert-manager plus the `local-ca` ClusterIssuer that
	// issues every local ingress cert from it. It exists so the local Traefik default
	// cert carries the real ${base_domain} SANs — making it verifiable against one
	// exported trust anchor instead of needing a skip-verify flag in every tool.
	// Ordered between cert-manager-config (whose `selfsigned` ClusterIssuer mints the
	// root) and the ingress layer (whose Certificate names this issuer).
	LocalCARootName    = "local-ca"
	DefaultLocalCAPath = "./flux/local/ca"
	// ExternalDNSRootName / DefaultExternalDNSPath is the DOKS DNS layer:
	// external-dns (Cloudflare). It is its own stack (controller-agnostic) and
	// DOKS-only — on kind the tunnel controller owns DNS.
	ExternalDNSRootName    = "external-dns"
	DefaultExternalDNSPath = "./flux/ingress/external-dns"
	// CloudflareAPITokenRootName / DefaultCloudflareAPITokenPath is the first half
	// of the ACME (Let's Encrypt) issuer layer: the Cloudflare DNS-01 token
	// ExternalSecret (cloudflare-api-token, in the cert-manager namespace) the
	// letsencrypt ClusterIssuer's solver reads. Split from the issuer itself so
	// its Secret is guaranteed synced before the issuer is applied. No ${...}
	// placeholders, so it carries no substitution.
	CloudflareAPITokenRootName    = "cloudflare-api-token"
	DefaultCloudflareAPITokenPath = "./flux/ingress/cloudflare-api-token"
	// LetsEncryptRootName / DefaultLetsEncryptPath is the ACME (Let's Encrypt)
	// ClusterIssuer, shared by both ACME --tls-issuer values (letsencrypt
	// production, staging CA) with the issuer name + ACME endpoint substituted per
	// cluster (${tls_issuer}, ${acme_server}). It dependsOn cloudflare-api-token
	// so the DNS-01 token Secret has synced before the issuer is applied:
	// cert-manager evaluates the solver on apply and pins the issuer InvalidSolver
	// — without re-reconciling — if the secret is missing, so the two must be
	// ordered, not co-applied. DOKS-only and deployed only for an ACME issuer;
	// selfsigned clusters use the always-present selfsigned issuer instead. The
	// Flux Kustomization is named "letsencrypt" on every cluster regardless of CA.
	LetsEncryptRootName    = "letsencrypt"
	DefaultLetsEncryptPath = "./flux/ingress/letsencrypt"
	// EchoRootName / DefaultEchoPath deploy the echo test backend. kind clusters
	// get it by default (after the ingress layer, so it is routable); it is
	// reusable on any cluster type via ./flux/echo.
	EchoRootName    = "echo"
	DefaultEchoPath = "./flux/echo"
	// MetricsRemoteRootName / DefaultMetricsRemotePath is the opt-in remote metrics
	// destination (--metrics-remote): a secondary in-cluster Prometheus receiver (a
	// Grafana-Cloud/remote stand-in for preview + local) plus a ConfigMap that adds
	// a second remote_write destination to the always-on k8s-monitoring Alloy, which
	// references it via an optional valuesFrom. Substitutes cluster-vars for the
	// receiver PVC (${storage_class}) and the destination's cluster external label
	// (${cluster_name}).
	MetricsRemoteRootName    = "metrics-remote"
	DefaultMetricsRemotePath = "./flux/test/prometheus"
	// GrafanaCloudRootName / DefaultGrafanaCloudPath is the opt-in Grafana Cloud
	// metrics destination (--grafana-cloud): an ESO ExternalSecret pulling the
	// GRAFANA_CLOUD_ACCESS_{URL,USER,TOKEN} trio from bitwarden into the
	// grafana-cloud-access Secret, plus a ConfigMap adding a GC remote_write
	// destination (url + basic auth from that Secret, curated by the same allowlist,
	// tagged ${cluster_name}/${env}) to the always-on k8s-monitoring Alloy via its
	// optional valuesFrom. The real-remote counterpart to the in-cluster
	// metrics-remote stand-in; used on a deliberately chosen cluster, never preview.
	GrafanaCloudRootName    = "grafana-cloud"
	DefaultGrafanaCloudPath = "./flux/grafana-cloud"
	// ESOConfigName is the nested Kustomization holding the bitwarden
	// ClusterSecretStore; the ingress layer dependsOn it (cross-layer) so its
	// ExternalSecrets can sync.
	ESOConfigName = "eso-config"
	// PrometheusName is the base-stack Kustomization (flux/cluster) that owns the
	// `monitoring` namespace and the local Prometheus store; the stacks living in that
	// namespace (k8s-monitoring, cost-exporter) dependsOn it.
	PrometheusName = "prometheus"
	// CertManagerConfigName is the nested Kustomization holding the selfsigned
	// ClusterIssuer; the Traefik ingress layer dependsOn it (cross-layer) so the
	// Certificate CRD is established and the selfsigned issuer exists.
	CertManagerConfigName = "cert-manager-config"

	// ClusterVarsName is the flux-system ConfigMap dctl writes at bootstrap with
	// this cluster's identity. Substituting reconcile roots pull it via
	// postBuild.substituteFrom, so the shared stacks (and downstream
	// Kustomizations) resolve ${source_kind}/${source_name}/${base_domain}/etc.
	// to per-cluster values. It is the single source of cluster-scoped variables.
	ClusterVarsName = "cluster-vars"
	// Cluster-vars keys. SourceKind/SourceName let the shared stacks bind their
	// sourceRef; BaseDomain/ClusterName are the cluster's ingress FQDN and name;
	// BitwardenOrgID/ProjectID scope the ESO ClusterSecretStore to a bws project.
	VarSourceKind         = "source_kind"
	VarSourceName         = "source_name"
	VarBaseDomain         = "base_domain"
	VarClusterName        = "cluster_name"
	VarBitwardenOrgID     = "bitwarden_org_id"
	VarBitwardenProjectID = "bitwarden_project_id"
	// VarTLSIssuer is the cert-manager ClusterIssuer name the DOKS Traefik default
	// certificate is issued by: TLSIssuerSelfSigned, TLSIssuerLetsEncrypt, or
	// TLSIssuerStaging. It also names the shared ACME ClusterIssuer and its
	// account-key secret (${tls_issuer}-account-key) in the letsencrypt layer.
	VarTLSIssuer = "tls_issuer"
	// VarACMEServer is the ACME directory endpoint the shared letsencrypt
	// ClusterIssuer registers against (${acme_server}) — the production or staging
	// Let's Encrypt CA, per ACMEServerURL(tls_issuer). Empty for selfsigned (the
	// ACME layer is not deployed), so nothing references it.
	VarACMEServer = "acme_server"
	// VarDNSZone is the cluster's Cloudflare zone apex (eTLD+1 of base_domain),
	// e.g. dantofa.dev. external-dns filters zones by their apex, so it must be
	// the registrable domain, not base_domain (a subdomain would exclude the zone).
	VarDNSZone = "dns_zone"
	// VarStorageClass is the Kubernetes StorageClass stateful stacks provision
	// their PVCs from (${storage_class}), written by dctl at bootstrap. It is per
	// cluster type — StorageClassDOKS or StorageClassLocal — so a portable manifest
	// binds to whichever the cluster provides (the class names differ, and a single
	// StorageClass manifest cannot span both provisioners).
	VarStorageClass = "storage_class"
	// VarEnv is the deployment environment label (${env}, e.g. local/preview/prod)
	// applied to metrics forwarded to a shared remote stack (Grafana Cloud) so one
	// stack can tell clusters apart alongside ${cluster_name}. Set from --env.
	VarEnv = "env"

	// Per-cluster cost constants the cost dashboard multiplies the always-collected
	// metrics by (node count, PV bytes, LB count, egress). dctl writes them at
	// bootstrap: compute + transfer from the DO Sizes API for the cluster's worker
	// size, storage + LB from DO's fixed published rates. On kind they default to 0
	// (no cloud cost) — see DefaultCostVars.
	VarNodeHourlyPrice       = "node_hourly_price"        // one node's $/hour
	VarNodeTransferGiB       = "node_transfer_gib"        // one node's included monthly transfer, GiB
	VarBlockStorageGiBHourly = "block_storage_gib_hourly" // $/GiB-hour for a PersistentVolume
	VarLBHourlyPrice         = "lb_hourly_price"          // $/hour per LoadBalancer Service

	// clusterVarsNamespace is where the ConfigMap and reconcile roots live.
	clusterVarsNamespace = "flux-system"

	// ESONamespace is where the External Secrets Operator and its secret-zero
	// live; BitwardenTokenSecret/Key is the machine-account token the
	// ClusterSecretStore authenticates to Bitwarden Secrets Manager with.
	ESONamespace         = "external-secrets-system"
	BitwardenTokenSecret = "bitwarden-access-token"
	BitwardenTokenKey    = "token"
)

// SourceType selects which Flux source kind a cluster is bootstrapped against.
// oci is the default; git stays a first-class option for downstream projects
// that would rather track a branch than publish OCI artifacts.
type SourceType string

const (
	SourceOCI SourceType = "oci"
	SourceGit SourceType = "git"

	// DefaultSourceType is what a bootstrap registers unless --source-type says
	// otherwise.
	DefaultSourceType = SourceOCI
)

// FluxKind maps the CLI-facing source type to the Flux source CRD kind used in a
// Kustomization's sourceRef.
func (t SourceType) FluxKind() string {
	if t == SourceGit {
		return "GitRepository"
	}
	return "OCIRepository"
}

// DefaultRevision is the source revision tracked when none is given: the latest
// OCI tag, or the default git branch.
func (t SourceType) DefaultRevision() string {
	if t == SourceGit {
		return DefaultSourceBranch
	}
	return DefaultOCIRevision
}

// Engine is the flux-CLI surface this package depends on, satisfied by the
// clients adapter. It installs Flux and registers sources; the cluster-vars
// ConfigMap and reconcile roots go through Applier instead (the flux CLI can't
// set postBuild.substituteFrom). Create operations are create-or-update
// (idempotent).
type Engine interface {
	Install(ctx context.Context, version string) error
	CreateGitSource(ctx context.Context, spec SourceSpec) error
	DeleteGitSource(ctx context.Context, name string) error
	CreateOCISource(ctx context.Context, spec SourceSpec) error
	DeleteOCISource(ctx context.Context, name string) error
	DeleteKustomization(ctx context.Context, name string) error
}

// ReconcileRoot is a top-level Flux Kustomization dctl applies as a CR during
// bootstrap. When Substitute is set it carries a postBuild.substituteFrom the
// cluster-vars ConfigMap, so the portable stacks it reconciles resolve
// ${source_kind}/${source_name}/${base_domain}/etc. to this cluster's values.
// DependsOn orders it after other roots. Both are things `flux create
// kustomization` can't express, so bootstrap goes through the kube adapter.
type ReconcileRoot struct {
	Name       string
	Path       string
	SourceKind string // OCIRepository | GitRepository (this cluster's source)
	SourceName string
	DependsOn  []string // reconcile-root names in flux-system to wait for
	// Substitute pulls cluster-vars via postBuild.substituteFrom so a portable
	// (source-agnostic) tree binds to this cluster's values. Leave it off for
	// source-pinned trees with no ${...} placeholders, to avoid running
	// substitution over them.
	Substitute bool
}

// Applier is the cluster-side surface bootstrap needs beyond the flux CLI:
// writing the cluster-vars ConfigMap and applying reconcile roots as
// Kustomization CRs (both create-or-update). Satisfied by the kube adapter.
type Applier interface {
	ApplyConfigMap(ctx context.Context, namespace, name string, data map[string]string) error
	ApplyReconcileRoot(ctx context.Context, root ReconcileRoot) error
}

// SecretApplier plants a bootstrap secret (ensure a namespace, create-or-update a
// Secret). Satisfied by the kube adapter.
type SecretApplier interface {
	EnsureNamespace(ctx context.Context, name string) error
	ApplySecret(ctx context.Context, namespace, name string, data map[string][]byte, annotations map[string]string) error
}

// SourceSecretStore is the cluster-side credential surface a private source
// needs: minting the Secret it authenticates with — an Opaque basic-auth Secret
// for git, a dockerconfigjson pull secret for OCI (the only shape an
// OCIRepository's secretRef accepts), both create-or-update — and checking that a
// referenced Secret is actually there. Satisfied by the kube adapter.
//
// This credential is secret-zero, like the Bitwarden access token: it cannot come
// from ESO, because ESO is itself reconciled from the very source the credential
// authenticates. That is why dctl plants it directly.
type SourceSecretStore interface {
	SecretExists(ctx context.Context, namespace, name string) (bool, error)
	ApplySecret(ctx context.Context, namespace, name string, data map[string][]byte, annotations map[string]string) error
	ApplyDockerConfigSecret(ctx context.Context, namespace, name string, config []byte) error
}

// BootstrapApplier is the full cluster-side surface Bootstrap needs: the
// cluster-vars ConfigMap and reconcile roots, plus the source credential when the
// platform source is private.
type BootstrapApplier interface {
	Applier
	SourceSecretStore
}

// TLS issuer names — the cert-manager ClusterIssuer the Traefik default cert is
// issued by, selected per cluster at bootstrap (--tls-issuer). SelfSigned pairs
// with Cloudflare Full (no rate limits, no external dep); LetsEncrypt pairs with
// Full (strict) (production: a publicly-trusted cert via DNS-01); Staging is the
// same ACME/DNS-01 flow against Let's Encrypt's staging CA (preview/CI: high
// rate limits, untrusted certs — exercises the ACME path without spending the
// production CA's limits on ephemeral per-PR clusters).
const (
	TLSIssuerSelfSigned  = "selfsigned"
	TLSIssuerLetsEncrypt = "letsencrypt"
	TLSIssuerStaging     = "staging"
)

// ACME directory endpoints the shared letsencrypt ClusterIssuer registers
// against, selected by --tls-issuer (see ACMEServerURL). Production issues
// publicly-trusted certs under tight rate limits; staging is untrusted with high
// limits, for ephemeral preview/CI clusters.
const (
	ACMEServerLetsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"
	ACMEServerStaging     = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// StorageClass names written as ${storage_class} per cluster type: DOKS ships the
// managed do-block-storage CSI class (default); kind ships the local-path
// "standard" class (default, already used by the local SeaweedFS store).
const (
	StorageClassDOKS  = "do-block-storage"
	StorageClassLocal = "standard"
)

// ACMEServerURL maps an ACME-backed --tls-issuer to its ACME directory endpoint,
// injected as the ${acme_server} cluster-var and substituted into the shared
// ClusterIssuer. Returns "" for a non-ACME issuer (selfsigned), which deploys no
// ACME layer, so the empty value is never referenced.
func ACMEServerURL(issuer string) string {
	switch issuer {
	case TLSIssuerLetsEncrypt:
		return ACMEServerLetsEncrypt
	case TLSIssuerStaging:
		return ACMEServerStaging
	default:
		return ""
	}
}

// CostVars formats the per-cluster cost constants into cluster-vars entries the
// cost dashboard's PromQL substitutes (${node_hourly_price} etc.). Kept here with
// the var keys so the DOKS and kind bootstraps produce identical entries; kind
// passes zeros (no cloud cost). 'f' formatting avoids scientific notation, which
// PromQL number literals reject.
func CostVars(nodeHourlyUSD, nodeTransferGiB, blockStorageGiBHourlyUSD, lbHourlyUSD float64) map[string]string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return map[string]string{
		VarNodeHourlyPrice:       f(nodeHourlyUSD),
		VarNodeTransferGiB:       f(nodeTransferGiB),
		VarBlockStorageGiBHourly: f(blockStorageGiBHourlyUSD),
		VarLBHourlyPrice:         f(lbHourlyUSD),
	}
}

// DNSZone returns the registrable domain (eTLD+1) of an ingress base_domain,
// e.g. "preview.dantofa.dev" -> "dantofa.dev", "dantofa.com" -> "dantofa.com".
// This is the cluster's Cloudflare zone apex: external-dns filters zones by apex
// (a subdomain filter excludes the parent zone), so it is derived from the one
// base_domain input rather than configured separately.
func DNSZone(baseDomain string) (string, error) {
	zone, err := publicsuffix.EffectiveTLDPlusOne(strings.TrimSuffix(baseDomain, "."))
	if err != nil {
		return "", fmt.Errorf("deriving DNS zone from base domain %q: %w", baseDomain, err)
	}
	return zone, nil
}

// ValidateTLSIssuer rejects an unknown --tls-issuer value. The name is also the
// ClusterIssuer name substituted into the Traefik Certificate, so it must match
// an issuer the cluster deploys (selfsigned is always present; letsencrypt and
// staging are each added only for their value, via ACMEReconcileRoots).
func ValidateTLSIssuer(issuer string) error {
	switch issuer {
	case TLSIssuerSelfSigned, TLSIssuerLetsEncrypt, TLSIssuerStaging:
		return nil
	default:
		return fmt.Errorf("--tls-issuer must be %q, %q, or %q, got %q",
			TLSIssuerSelfSigned, TLSIssuerLetsEncrypt, TLSIssuerStaging, issuer)
	}
}

// ACMEReconcileRoots returns the reconcile roots that deploy the ACME
// ClusterIssuer for an ACME-backed --tls-issuer (letsencrypt or staging), and
// ok=false for selfsigned (no ACME layer; the selfsigned issuer ships in
// cert-manager-config). Both ACME issuers share the same two roots — same names
// and paths on every cluster — with the issuer identity carried by substitution
// (${tls_issuer}, ${acme_server}). The roots are returned in dependency order:
//
//  1. cloudflare-api-token — the Cloudflare DNS-01 token ExternalSecret. Needs
//     the bitwarden store (eso-config) to sync and the cert-manager namespace it
//     targets (cert-manager-config). No ${...} placeholders, so no substitution.
//  2. letsencrypt — the ClusterIssuer itself. Needs the ClusterIssuer CRD
//     (cert-manager-config) and, crucially, the token Secret already synced, so
//     it dependsOn cloudflare-api-token: cert-manager pins a freshly-applied
//     issuer InvalidSolver — without re-reconciling — if the secret is absent, so
//     the two must be ordered, not co-applied. Substituted for the issuer identity.
//
// The Traefik Certificate resolves against the issuer asynchronously once it is
// Ready. Callers must have passed ValidateTLSIssuer first, so an unknown value
// cannot reach here.
func ACMEReconcileRoots(issuer string) ([]ReconcileRoot, bool) {
	switch issuer {
	case TLSIssuerLetsEncrypt, TLSIssuerStaging:
		return []ReconcileRoot{
			{
				Name:      CloudflareAPITokenRootName,
				Path:      DefaultCloudflareAPITokenPath,
				DependsOn: []string{CertManagerConfigName, ESOConfigName},
			},
			{
				Name:       LetsEncryptRootName,
				Path:       DefaultLetsEncryptPath,
				DependsOn:  []string{CertManagerConfigName, CloudflareAPITokenRootName},
				Substitute: true,
			},
		}, true
	default:
		return nil, false
	}
}

// DOKSIngressRoots returns the ingress-layer reconcile roots for a DOKS cluster.
//
// The default (dolb=false) is the Cloudflare Tunnel controller — the same
// outbound-only, LoadBalancer-free ingress kind clusters use (DefaultTunnelIngressPath):
// Cloudflare terminates edge TLS and the controller owns DNS, so there is no DO
// LoadBalancer (the bulk of the cluster's cost), no external-dns, and no
// ACME/cert-manager cert. tlsIssuer is inert in this mode. The single root
// dependsOn eso-config (the controller pulls its cloudflare-api secret from
// bitwarden via ESO) and substitutes cluster-vars — tunnel_name is ${cluster_name},
// which the teardown tunnel-reap (ResolveTunnel/DeleteTunnelByName) resolves the
// tunnel by, so the DOKS tunnel lifecycle matches local's.
//
// With --dolb (dolb=true) it returns the LoadBalancer path unchanged: external-dns
// (Cloudflare DNS from Ingress status) + the ACME issuer layer when tlsIssuer is an
// ACME value (letsencrypt/staging) + Traefik (the default IngressClass, behind a DO
// LoadBalancer locked to Cloudflare's IPs). tlsIssuer names the ClusterIssuer the
// Traefik default cert is issued by; callers must ValidateTLSIssuer first.
func DOKSIngressRoots(dolb bool, tlsIssuer string) []ReconcileRoot {
	if !dolb {
		return []ReconcileRoot{
			{
				Name:       IngressRootName,
				Path:       DefaultTunnelIngressPath,
				DependsOn:  []string{ESOConfigName},
				Substitute: true,
			},
		}
	}
	// Traefik (ingress) and external-dns (DNS) are separate stacks. Traefik's
	// default cert is issued by cert-manager (${tls_issuer} ClusterIssuer), so it
	// waits on cert-manager-config (Certificate CRD + the always-present selfsigned
	// issuer); external-dns pulls its Cloudflare token from bws, so it waits on
	// eso-config.
	ingress := ReconcileRoot{
		Name:       IngressRootName,
		Path:       DefaultRemoteIngressPath,
		DependsOn:  []string{CertManagerConfigName},
		Substitute: true,
	}
	roots := []ReconcileRoot{
		{
			Name:       ExternalDNSRootName,
			Path:       DefaultExternalDNSPath,
			DependsOn:  []string{ESOConfigName},
			Substitute: true,
		},
	}
	// The ACME issuer layer (letsencrypt production or the staging CA) is added
	// only when the Traefik cert is issued by one; selfsigned needs none (it ships
	// in cert-manager-config). The ingress layer must also wait on the issuer: the
	// Traefik Certificate names the ${tls_issuer} ClusterIssuer, so that issuer
	// must exist before the Certificate is applied — else the CertificateRequest
	// fails IssuerNotFound and issuance stalls (the ACME issuer lives in its own
	// root, unlike the selfsigned one).
	if acme, ok := ACMEReconcileRoots(tlsIssuer); ok {
		ingress.DependsOn = append(ingress.DependsOn, LetsEncryptRootName)
		roots = append(roots, acme...)
	}
	return append(roots, ingress)
}

// MetricsRemoteReconcileRoot returns the reconcile root for the opt-in remote
// metrics destination (--metrics-remote). It deploys a secondary Prometheus
// receiver and the k8s-monitoring-remote-destination ConfigMap that the always-on
// k8s-monitoring HelmRelease merges via valuesFrom, adding a second remote_write
// destination (the base stack keeps only the local destination when this root is
// absent). dependsOn prometheus for the `monitoring` namespace and the
// prometheus-community HelmRepository it reuses; substitutes cluster-vars for the
// receiver PVC (${storage_class}) and the cluster external label (${cluster_name}).
func MetricsRemoteReconcileRoot() ReconcileRoot {
	return ReconcileRoot{
		Name:       MetricsRemoteRootName,
		Path:       DefaultMetricsRemotePath,
		DependsOn:  []string{PrometheusName},
		Substitute: true,
	}
}

// GrafanaCloudReconcileRoot returns the reconcile root for the opt-in Grafana
// Cloud metrics destination (--grafana-cloud). It creates the ESO ExternalSecret
// (bitwarden → grafana-cloud-access) and the ConfigMap that the always-on
// k8s-monitoring HelmRelease merges via valuesFrom, forwarding a curated,
// ${cluster_name}/${env}-labelled subset to Grafana Cloud. dependsOn eso-config
// (the ExternalSecret needs the bitwarden ClusterSecretStore) and prometheus (the
// `monitoring` namespace); substitutes cluster-vars for the external labels.
func GrafanaCloudReconcileRoot() ReconcileRoot {
	return ReconcileRoot{
		Name:       GrafanaCloudRootName,
		Path:       DefaultGrafanaCloudPath,
		DependsOn:  []string{ESOConfigName, PrometheusName},
		Substitute: true,
	}
}

// ValidateBitwardenConfig guards against a half-configured Bitwarden setup. When
// a ClusterSecretStore is being scoped (a project or organization ID is given)
// but the machine-account token is missing, ESO can never authenticate, secret-
// zero is skipped, and every stack behind eso-config hangs until Flux times out
// minutes later. Fail fast at bootstrap with an actionable message instead. An
// entirely empty trio is allowed (bitwarden simply not configured for the
// cluster), matching ProvisionESOAccessToken's no-op-on-empty contract.
func ValidateBitwardenConfig(token, projectID, orgID string) error {
	if token != "" || (projectID == "" && orgID == "") {
		return nil
	}
	return errors.New("bitwarden project/organization configured but no access token: " +
		"set --bitwarden-token or $BWS_ACCESS_TOKEN (note: `bws run` strips " +
		"BWS_ACCESS_TOKEN from the child environment, so pass it explicitly)")
}

// ProvisionESOAccessToken plants secret-zero: the Bitwarden machine-account token
// the ESO ClusterSecretStore authenticates with. Idempotent; a no-op when token
// is empty (bitwarden not configured for this cluster, so the store stays
// unauthenticated and its ExternalSecrets will not sync).
func ProvisionESOAccessToken(ctx context.Context, a SecretApplier, token string) error {
	if token == "" {
		return nil
	}
	if err := a.EnsureNamespace(ctx, ESONamespace); err != nil {
		return err
	}
	return a.ApplySecret(ctx, ESONamespace, BitwardenTokenSecret,
		map[string][]byte{BitwardenTokenKey: []byte(token)}, nil)
}

// SourceSpec describes a Flux source to register: its Type (oci/git) selects the
// source CRD kind and how Revision is read (an OCI tag or a git branch).
// Insecure allows plain-HTTP OCI, for the in-cluster kind registry only.
//
// SecretRef/Token/Username authenticate the source against a private repository
// or registry. They are declarative like every other field: `source create` is a
// create-or-update over the whole object, so a later call that omits them strips
// the credential off an existing source. Pass them on every invocation.
type SourceSpec struct {
	Type     SourceType
	Name     string
	URL      string
	Revision string
	Insecure bool
	// SecretRef names an existing Secret in flux-system the source authenticates
	// with (spec.secretRef). Empty leaves the source anonymous — it can then only
	// track a publicly readable repository/registry. With Token set, this instead
	// names the Secret to mint (default: "<Name>-auth").
	SecretRef string
	// Token, when set, mints the credential Secret as part of registering the
	// source: HTTPS basic auth for git (Username + Token), a dockerconfigjson pull
	// secret for OCI. Leave it empty to reference a Secret created out of band —
	// the only option for SSH deploy keys, which need a known_hosts host-key scan
	// and an out-of-band registration step (`flux create secret git`).
	Token string
	// Username is the basic-auth user paired with Token. Empty defaults to
	// DefaultSourceUsername, which is what a forge expects alongside a PAT.
	Username string
}

// SourceAuth defaults and conventions for an authenticated source.
const (
	// DefaultSourceUsername is the basic-auth username paired with a --token when
	// none is given. GitHub/GitLab ignore the username for a PAT but require a
	// non-empty one, and "git" is the conventional placeholder.
	DefaultSourceUsername = "git"
	// sourceSecretSuffix builds the default minted-Secret name, "<source>-auth",
	// so a source and its credential stay associated without the caller naming it.
	sourceSecretSuffix = "-auth"
)

// SecretName is the Secret this spec's source authenticates with: SecretRef when
// given, else the derived "<name>-auth" when a Token is being minted, else "" for
// an anonymous source.
func (s SourceSpec) SecretName() string {
	if s.SecretRef != "" {
		return s.SecretRef
	}
	if s.Token != "" {
		return s.Name + sourceSecretSuffix
	}
	return ""
}

// BasicAuthUsername is the username minted alongside Token.
func (s SourceSpec) BasicAuthUsername() string {
	if s.Username != "" {
		return s.Username
	}
	return DefaultSourceUsername
}

// Validate rejects source specs whose auth fields cannot be honoured, before
// anything is written to the cluster.
func (s SourceSpec) Validate() error {
	if s.Token == "" {
		return nil
	}
	if s.Type == SourceGit && isSSHURL(s.URL) {
		return fmt.Errorf("--token is HTTPS basic auth and cannot authenticate the SSH URL %q: "+
			"create an SSH deploy-key secret out of band (`flux create secret git <name> --url %s`, "+
			"which scans the host key and prints the key to register on the forge) "+
			"and reference it with --secret-ref", s.URL, s.URL)
	}
	return nil
}

// isSSHURL reports whether a git URL is fetched over SSH rather than HTTPS —
// either an ssh:// scheme or the scp-like "git@host:org/repo" form.
func isSSHURL(url string) bool {
	if strings.HasPrefix(url, "ssh://") {
		return true
	}
	// scp-like syntax: a user@host prefix before the first "/" (which would
	// otherwise be a scheme's "//").
	at := strings.Index(url, "@")
	return at > 0 && !strings.Contains(url[:at], "/")
}

// RegistryHost extracts the registry a dockerconfigjson entry is keyed by from an
// OCI source URL: "oci://ghcr.io/dantofa/platform" -> "ghcr.io". A host:port
// registry (the in-cluster kind registry) is preserved intact.
func RegistryHost(ociURL string) (string, error) {
	host := strings.TrimPrefix(ociURL, "oci://")
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return "", fmt.Errorf("cannot determine the registry host from OCI URL %q", ociURL)
	}
	return host, nil
}

// DockerConfigJSON renders the .dockerconfigjson payload of an image pull secret
// for one registry. The "auth" field is the base64 of "user:password" — the field
// registries actually read, with username/password kept alongside for the clients
// that prefer them.
func DockerConfigJSON(registry, username, password string) ([]byte, error) {
	type entry struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Auth     string `json:"auth"`
	}
	cfg := struct {
		Auths map[string]entry `json:"auths"`
	}{
		Auths: map[string]entry{
			registry: {
				Username: username,
				Password: password,
				Auth:     base64.StdEncoding.EncodeToString([]byte(username + ":" + password)),
			},
		},
	}
	return json.Marshal(cfg)
}

// KustomizationSpec describes a Kustomization reconciling a path from a source.
// Type selects the source CRD kind (oci/git) the sourceRef points at. Substitute
// and DependsOn are the reconcile-root capabilities exposed to callers (a
// downstream project layering its own payload): Substitute binds ${...} tokens
// in the manifests to cluster-vars (e.g. ${base_domain}); DependsOn orders the
// payload after platform layers (e.g. "ingress") so it is not applied early.
type KustomizationSpec struct {
	Type       SourceType
	Name       string
	Source     string
	Path       string
	DependsOn  []string
	Substitute bool
}

// SourceResult reports a registered source. SecretRef is the credential it
// authenticates with, omitted for an anonymous (public) source.
type SourceResult struct {
	Source    string `json:"source"`
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	Revision  string `json:"revision"`
	SecretRef string `json:"secret_ref,omitempty"`
}

// KustomizationResult reports a registered kustomization.
type KustomizationResult struct {
	Kustomization string `json:"kustomization"`
	SourceKind    string `json:"source_kind"`
	Source        string `json:"source"`
	Path          string `json:"path"`
}

// BootstrapResult reports the source and reconcile roots a bootstrap registered.
type BootstrapResult struct {
	Source         string   `json:"source"`
	SourceKind     string   `json:"source_kind"`
	URL            string   `json:"url"`
	Revision       string   `json:"revision"`
	Kustomizations []string `json:"kustomizations"`
}

// AddSource registers (create-or-update) an OCIRepository or GitRepository source
// per spec.Type. When spec carries a Token it first mints the credential Secret
// the source will reference — in that order, so the source never reconciles
// against a secretRef that does not resolve yet; when it only references one, that
// Secret is checked to exist first. s may be nil only for an anonymous source
// (no credential to mint or check).
func AddSource(ctx context.Context, e Engine, s SourceSecretStore, spec SourceSpec) (SourceResult, error) {
	if err := spec.Validate(); err != nil {
		return SourceResult{}, err
	}
	// Resolve the credential name once so the minted Secret and the source's
	// secretRef cannot disagree.
	spec.SecretRef = spec.SecretName()
	switch {
	case spec.Token != "":
		if s == nil {
			return SourceResult{}, errors.New("--token needs cluster access to mint the credential secret")
		}
		if err := mintSourceSecret(ctx, s, spec); err != nil {
			return SourceResult{}, err
		}
	case spec.SecretRef != "":
		if s == nil {
			return SourceResult{}, errors.New("--secret-ref needs cluster access to check the credential secret")
		}
		if err := checkSourceSecret(ctx, s, spec); err != nil {
			return SourceResult{}, err
		}
	}
	switch spec.Type {
	case SourceGit:
		if err := e.CreateGitSource(ctx, spec); err != nil {
			return SourceResult{}, err
		}
	default:
		if err := e.CreateOCISource(ctx, spec); err != nil {
			return SourceResult{}, err
		}
	}
	return SourceResult{
		Source: spec.Name, Kind: spec.Type.FluxKind(), URL: spec.URL,
		Revision: spec.Revision, SecretRef: spec.SecretRef,
	}, nil
}

// mintSourceSecret writes the credential spec.SecretRef names, in the shape the
// source kind requires: username/password keys for a GitRepository, a
// dockerconfigjson pull secret for an OCIRepository.
func mintSourceSecret(ctx context.Context, s SourceSecretStore, spec SourceSpec) error {
	if spec.Type == SourceGit {
		return s.ApplySecret(ctx, clusterVarsNamespace, spec.SecretRef, map[string][]byte{
			"username": []byte(spec.BasicAuthUsername()),
			"password": []byte(spec.Token),
		}, nil)
	}
	registry, err := RegistryHost(spec.URL)
	if err != nil {
		return err
	}
	config, err := DockerConfigJSON(registry, spec.BasicAuthUsername(), spec.Token)
	if err != nil {
		return err
	}
	return s.ApplyDockerConfigSecret(ctx, clusterVarsNamespace, spec.SecretRef, config)
}

// checkSourceSecret fails fast when --secret-ref names a Secret that is not there.
// The flux CLI does not validate the reference: it writes the source and then
// blocks until the object is Ready, so a typo'd name otherwise costs the CLI's
// full readiness timeout before surfacing as a source-controller error.
func checkSourceSecret(ctx context.Context, s SourceSecretStore, spec SourceSpec) error {
	exists, err := s.SecretExists(ctx, clusterVarsNamespace, spec.SecretRef)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	hint := "`flux create secret git " + spec.SecretRef + " --url " + spec.URL + "`"
	if spec.Type != SourceGit {
		hint = "`flux create secret oci " + spec.SecretRef + " --url <registry> -u <user> -p <token>`"
	}
	return fmt.Errorf("secret %q not found in namespace %q: create it first (%s), "+
		"or pass --token to have dctl mint it", spec.SecretRef, clusterVarsNamespace, hint)
}

// RemoveSource deletes a source of the given type.
func RemoveSource(ctx context.Context, e Engine, typ SourceType, name string) error {
	if typ == SourceGit {
		return e.DeleteGitSource(ctx, name)
	}
	return e.DeleteOCISource(ctx, name)
}

// AddKustomization registers (create-or-update) a Kustomization referencing a
// source of spec.Type. It applies a reconcile-root CR through the kube Applier
// (not `flux create kustomization`) so it can carry postBuild.substituteFrom and
// dependsOn — the capabilities a downstream payload needs, and the same CR shape
// bootstrap's own roots use.
func AddKustomization(ctx context.Context, a Applier, spec KustomizationSpec) (KustomizationResult, error) {
	kind := spec.Type.FluxKind()
	root := ReconcileRoot{
		Name:       spec.Name,
		Path:       spec.Path,
		SourceKind: kind,
		SourceName: spec.Source,
		DependsOn:  spec.DependsOn,
		Substitute: spec.Substitute,
	}
	if err := a.ApplyReconcileRoot(ctx, root); err != nil {
		return KustomizationResult{}, err
	}
	return KustomizationResult{
		Kustomization: spec.Name, SourceKind: kind, Source: spec.Source, Path: spec.Path,
	}, nil
}

// RemoveKustomization deletes a Kustomization.
func RemoveKustomization(ctx context.Context, e Engine, name string) error {
	return e.DeleteKustomization(ctx, name)
}

// KustomizationStatus is one Flux Kustomization's reconciliation state. Status
// is the kstatus verdict (Current/InProgress/Failed/...); Ready is the gate.
// Message is kstatus's own synthesized text, which describes the object (e.g.
// a generation/observedGeneration mismatch) rather than the failure; when
// Flux has written one, ConditionMessage is the Ready condition's own message
// -- the actual reason, once a health check has run long enough to record it.
type KustomizationStatus struct {
	Namespace        string `json:"namespace"`
	Name             string `json:"name"`
	Status           string `json:"status"`
	Ready            bool   `json:"ready"`
	Message          string `json:"message,omitempty"`
	ConditionMessage string `json:"condition_message,omitempty"`
}

// KustomizationStatuser reads the reconciliation status of the Flux
// Kustomizations on a cluster (satisfied by the kube adapter, via kstatus).
type KustomizationStatuser interface {
	KustomizationStatuses(ctx context.Context, namespace string) ([]KustomizationStatus, error)
}

// HelmReleaseStatus is one Flux HelmRelease's reconciliation state, scoped to
// what a verify wait needs to fail fast: Terminal is true when the release
// has failed outright (an install/upgrade/uninstall that will not self-heal)
// rather than merely progressing. Reason/Message are the Ready condition's
// own, and Events carries the namespace's recent Warning events when
// Terminal -- often the only place the underlying cause (e.g. an admission
// webhook refusal) is recorded.
type HelmReleaseStatus struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Reason    string   `json:"reason,omitempty"`
	Message   string   `json:"message,omitempty"`
	Terminal  bool     `json:"terminal"`
	Events    []string `json:"events,omitempty"`
}

// HelmReleaseStatuser reads the reconciliation status of the Flux
// HelmReleases on a cluster (satisfied by the kube adapter).
type HelmReleaseStatuser interface {
	HelmReleaseStatuses(ctx context.Context, namespace string) ([]HelmReleaseStatus, error)
}

// TerminalHelmReleaseError reports a HelmRelease that failed terminally under
// a waited-on Kustomization -- the cause a verify timeout would otherwise
// swallow, attached directly instead of requiring a diagnostics artifact.
type TerminalHelmReleaseError struct {
	Namespace, Name, Reason, Message string
	Events                           []string
}

func (e *TerminalHelmReleaseError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "HelmRelease %s/%s failed terminally (%s): %s", e.Namespace, e.Name, e.Reason, e.Message)
	for _, ev := range e.Events {
		fmt.Fprintf(&b, "\n  %s", ev)
	}
	return b.String()
}

// ListKustomizations returns every Kustomization's status (never nil, so an empty
// cluster renders as a JSON `[]`).
func ListKustomizations(ctx context.Context, s KustomizationStatuser, namespace string) ([]KustomizationStatus, error) {
	statuses, err := s.KustomizationStatuses(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if statuses == nil {
		statuses = []KustomizationStatus{}
	}
	return statuses, nil
}

// VerifyKustomizations returns every Kustomization's status plus whether all are
// ready — the gate: ok is false if any Kustomization is not reconciled.
func VerifyKustomizations(ctx context.Context, s KustomizationStatuser, namespace string) (statuses []KustomizationStatus, ok bool, err error) {
	statuses, err = ListKustomizations(ctx, s, namespace)
	if err != nil {
		return nil, false, err
	}
	ok = true
	for _, st := range statuses {
		if !st.Ready {
			ok = false
		}
	}
	return statuses, ok, nil
}

// VerifyKustomizationsWait polls VerifyKustomizations until every Kustomization
// is ready or the timeout elapses, returning the last statuses + ok either way
// (so a timed-out gate still reports what is not reconciled). It turns the
// snapshot gate into a convergence gate for CI after a bootstrap/apply.
//
// hs, when non-nil, is checked on every poll for a terminally-failed
// HelmRelease (one that will not self-heal, as opposed to one still
// progressing) and returns immediately with a TerminalHelmReleaseError the
// moment one appears -- instead of waiting out the rest of timeout only to
// report a generic "did not become ready" with the real cause left in Flux's
// own conditions. Pass nil to skip this (the plain Kustomization-only wait).
func VerifyKustomizationsWait(ctx context.Context, s KustomizationStatuser, hs HelmReleaseStatuser, namespace string, timeout, interval time.Duration) (statuses []KustomizationStatus, ok bool, err error) {
	deadline := time.Now().Add(timeout)
	// lastErr holds a transient failure so it can still be reported if the wait
	// ends without ever recovering. A dropped connection mid-wait must not end the
	// wait: a managed API server closes one now and then, and this loop exists
	// precisely to outlast conditions that resolve themselves.
	var lastErr error
	for {
		// statuses/ok are only updated on a successful poll, so a blip on the last
		// one does not erase the report the caller would otherwise have shown.
		got, allReady, err := VerifyKustomizations(ctx, s, namespace)
		switch {
		case err == nil:
			statuses, ok, lastErr = got, allReady, nil
			if ok {
				return statuses, true, nil
			}
		case retry.Transient(err):
			lastErr = err
		default:
			// A definitive answer (RBAC, a bad kubeconfig): retrying it would only
			// spend the caller's whole timeout to report the same thing.
			return nil, false, err
		}
		if hs != nil {
			if termErr := terminalHelmReleaseErr(ctx, hs, namespace); termErr != nil {
				return statuses, false, termErr
			}
		}
		if !time.Now().Before(deadline) {
			return statuses, ok && lastErr == nil, lastErr
		}
		select {
		case <-ctx.Done():
			return statuses, false, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// terminalHelmReleaseErr returns the first terminally-failed HelmRelease as an
// error, or nil if none is found. A read error is swallowed rather than
// propagated: this is a best-effort fail-fast layered on top of the
// Kustomization-level wait above, which already has its own retry/timeout
// handling for a transient API hiccup -- this check must not short-circuit
// that on a blip of its own.
func terminalHelmReleaseErr(ctx context.Context, hs HelmReleaseStatuser, namespace string) error {
	statuses, err := hs.HelmReleaseStatuses(ctx, namespace)
	if err != nil {
		return nil
	}
	for _, s := range statuses {
		if s.Terminal {
			return &TerminalHelmReleaseError{
				Namespace: s.Namespace, Name: s.Name,
				Reason: s.Reason, Message: s.Message, Events: s.Events,
			}
		}
	}
	return nil
}

// Bootstrap installs Flux, registers the source (oci or git per src.Type), writes
// the cluster-vars ConfigMap (this cluster's source coordinates merged with the
// caller's vars, e.g. base_domain/cluster_name), then applies the given reconcile
// roots as Kustomization CRs in order. Each root's SourceKind/SourceName are
// filled from the registered source, so callers pass roots describing only the
// paths and ordering. This one sequence serves every cluster: DOKS passes a
// single `cluster` root, kind passes `local-requirements` then `cluster`.
func Bootstrap(ctx context.Context, e Engine, a BootstrapApplier, version string, src SourceSpec, vars map[string]string, roots []ReconcileRoot) (BootstrapResult, error) {
	// Validate before Install: a bad source credential should fail the command
	// without having installed controllers into the cluster first.
	if err := src.Validate(); err != nil {
		return BootstrapResult{}, err
	}
	if err := e.Install(ctx, version); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := AddSource(ctx, e, a, src); err != nil {
		return BootstrapResult{}, err
	}
	kind := src.Type.FluxKind()

	// The cluster-vars ConfigMap the substituting roots read: the source
	// coordinates (always) plus the caller's cluster identity, which the roots
	// must be able to resolve before they reconcile.
	clusterVars := map[string]string{VarSourceKind: kind, VarSourceName: src.Name}
	for k, v := range vars {
		clusterVars[k] = v
	}
	if err := a.ApplyConfigMap(ctx, clusterVarsNamespace, ClusterVarsName, clusterVars); err != nil {
		return BootstrapResult{}, err
	}

	names := make([]string, 0, len(roots))
	for _, r := range roots {
		r.SourceKind, r.SourceName = kind, src.Name
		if err := a.ApplyReconcileRoot(ctx, r); err != nil {
			return BootstrapResult{}, err
		}
		names = append(names, r.Name)
	}
	return BootstrapResult{
		Source: src.Name, SourceKind: kind, URL: src.URL,
		Revision: src.Revision, Kustomizations: names,
	}, nil
}
