package teardown

import (
	"context"
	"fmt"
	"time"

	"github.com/dantofa/platform/internal/core/edgecert"
	fluxcore "github.com/dantofa/platform/internal/core/flux"
	"github.com/dantofa/platform/internal/core/retry"
)

// clusterVarsNamespace is where the cluster-vars ConfigMap lives (mirrors the
// flux-system namespace bootstrap writes it to).
const clusterVarsNamespace = "flux-system"

// Keys in the cloudflare-api secret. api_token is present in every ingress stack;
// account_id / tunnel_name only in the local tunnel stack (mirrors the tunnel
// ExternalSecret in flux/ingress/tunnel).
const (
	cloudflareTokenKey  = "api_token"
	cloudflareAccountID = "account_id"
	cloudflareTunnel    = "tunnel_name"
)

// tunnelSecret is the local tunnel controller's cloudflare-api secret (the only
// place account_id / tunnel_name live).
var tunnelSecret = SecretRef{Namespace: "cloudflare-tunnel-system", Name: "cloudflare-api"}

// SecretRef locates an in-cluster Secret.
type SecretRef struct{ Namespace, Name string }

// CloudflareSecretLocations are the cloudflare-api secret's possible homes, tried
// in order: the DOKS external-dns namespace, then the local tunnel controller's.
// They mirror the ExternalSecret targets in flux/ingress/{external-dns,tunnel}.
var CloudflareSecretLocations = []SecretRef{
	{Namespace: "external-dns", Name: "cloudflare-api"},
	{Namespace: "cloudflare-tunnel-system", Name: "cloudflare-api"},
}

// ClusterReader reads the cluster-side config teardown needs to find its inputs:
// the Cloudflare token (a Secret) and the DNS zone (the cluster-vars ConfigMap).
// Satisfied by the kube adapter.
type ClusterReader interface {
	SecretValue(ctx context.Context, namespace, name, key string) (string, error)
	ConfigMapValue(ctx context.Context, namespace, name, key string) (string, error)
}

// Retry budget for the cluster reads below. These are single quick lookups, so a
// short window is enough to ride out a dropped connection; a definitive error is
// not retried at all and still fails immediately.
const (
	readTimeout = 30 * time.Second
	readDelay   = 2 * time.Second
)

// retryingReader rides out transport-level failures on the reads that decide
// whether a teardown can proceed. Without it a single dropped connection aborts
// `cluster delete` before the cluster is destroyed — which does not fail safe: it
// leaves a running cluster behind, billing, until someone notices.
type retryingReader struct {
	inner   ClusterReader
	timeout time.Duration
	delay   time.Duration
}

// WithRetry wraps a ClusterReader so its reads survive a transient connection
// failure, retrying for up to timeout with delay between attempts. Applied inside
// Run with the readTimeout/readDelay budget, so every caller gets it.
func WithRetry(r ClusterReader, timeout, delay time.Duration) ClusterReader {
	return retryingReader{inner: r, timeout: timeout, delay: delay}
}

func (r retryingReader) SecretValue(ctx context.Context, namespace, name, key string) (string, error) {
	var v string
	err := retry.Do(ctx, r.timeout, r.delay, func() (err error) {
		v, err = r.inner.SecretValue(ctx, namespace, name, key)
		return err
	})
	return v, err
}

func (r retryingReader) ConfigMapValue(ctx context.Context, namespace, name, key string) (string, error) {
	var v string
	err := retry.Do(ctx, r.timeout, r.delay, func() (err error) {
		v, err = r.inner.ConfigMapValue(ctx, namespace, name, key)
		return err
	})
	return v, err
}

// ResolveCloudflareToken returns the Cloudflare API token from the in-cluster
// cloudflare-api secret (the same one ESO syncs for the ingress controller), so
// teardown reuses the cluster's own scoped credential rather than a flag. Returns
// "" (no error) when no cloudflare-api secret exists: the ingress stack was never
// bootstrapped, so nothing was provisioned in Cloudflare and there is nothing to
// tear down (a partially-created or leftover cluster is still safely deletable).
func ResolveCloudflareToken(ctx context.Context, r ClusterReader) (string, error) {
	for _, loc := range CloudflareSecretLocations {
		v, err := r.SecretValue(ctx, loc.Namespace, loc.Name, cloudflareTokenKey)
		if err != nil {
			return "", err
		}
		if v != "" {
			return v, nil
		}
	}
	return "", nil
}

// ResolveZone returns the cluster's Cloudflare zone apex from the cluster-vars
// ConfigMap (the dns_zone bootstrap derived from base_domain). Falls back to
// deriving it from base_domain when dns_zone is absent — so a cluster
// bootstrapped before dns_zone was written can still be torn down.
func ResolveZone(ctx context.Context, r ClusterReader) (string, error) {
	zone, err := r.ConfigMapValue(ctx, clusterVarsNamespace, fluxcore.ClusterVarsName, fluxcore.VarDNSZone)
	if err != nil {
		return "", err
	}
	if zone != "" {
		return zone, nil
	}
	base, err := r.ConfigMapValue(ctx, clusterVarsNamespace, fluxcore.ClusterVarsName, fluxcore.VarBaseDomain)
	if err != nil {
		return "", err
	}
	if base == "" {
		return "", fmt.Errorf("neither %s nor %s in the %s ConfigMap; cannot determine the DNS zone to drain",
			fluxcore.VarDNSZone, fluxcore.VarBaseDomain, fluxcore.ClusterVarsName)
	}
	return fluxcore.DNSZone(base)
}

// TunnelRef identifies a Cloudflare Tunnel to reap: the account it lives in and
// its name (the controller names it after the cluster).
type TunnelRef struct {
	AccountID string
	Name      string
}

// ResolveTunnel returns the cluster's Cloudflare Tunnel coordinates if it has one
// (ok=false on a cluster without the tunnel controller, e.g. DOKS). Both the
// account id and tunnel name must be present in the tunnel controller's
// cloudflare-api secret.
func ResolveTunnel(ctx context.Context, r ClusterReader) (TunnelRef, bool, error) {
	account, err := r.SecretValue(ctx, tunnelSecret.Namespace, tunnelSecret.Name, cloudflareAccountID)
	if err != nil {
		return TunnelRef{}, false, err
	}
	name, err := r.SecretValue(ctx, tunnelSecret.Namespace, tunnelSecret.Name, cloudflareTunnel)
	if err != nil {
		return TunnelRef{}, false, err
	}
	if account == "" || name == "" {
		return TunnelRef{}, false, nil
	}
	return TunnelRef{AccountID: account, Name: name}, true, nil
}

// Run is the full teardown entrypoint a delete command calls: it resolves the
// Cloudflare token and zone from the cluster, builds the Cloudflare client via
// newCF, drains the ingress DNS records, deletes the cluster's edge certificate
// through certs (nil when the caller has no certificate credential or the cluster
// type has no edge: nothing is deleted and nothing is reported), and — on a cluster with a Cloudflare
// Tunnel — stops the tunnel controller and deletes the leftover tunnel object.
// Keeping this in core lets the DOKS and local delete commands stay thin and
// share the exact same flow (the tunnel steps are simply no-ops on DOKS).
func Run(ctx context.Context, r ClusterReader, k KubeAPI, newCF func(token string) (CloudflareAPI, error), certs edgecert.API, timeout, interval time.Duration) (Result, error) {
	r = WithRetry(r, readTimeout, readDelay)
	token, err := ResolveCloudflareToken(ctx, r)
	if err != nil {
		return Result{}, err
	}
	if token == "" {
		// No cloudflare-api secret: the ingress stack was never bootstrapped, so
		// nothing was provisioned in Cloudflare to reap. Skip so a leftover or
		// partially-created cluster is deletable without --force.
		return Result{Skipped: true}, nil
	}
	zone, err := ResolveZone(ctx, r)
	if err != nil {
		return Result{}, err
	}
	cfapi, err := newCF(token)
	if err != nil {
		return Result{}, fmt.Errorf("building cloudflare client: %w", err)
	}

	res, err := Teardown(ctx, k, cfapi, Options{Zone: zone, Timeout: timeout, Interval: interval})
	// Reap the cluster's edge certificate whether or not the drain succeeded: a
	// --force delete that skipped it would leak the certificate for good.
	res.EdgeCertsDeleted, res.EdgeCertError = reapEdgeCert(ctx, r, certs, zone)
	if err != nil {
		return res, err
	}

	// Reap the leftover Cloudflare Tunnel object (local clusters only). Records
	// are already drained above while the controller was alive; now stop the
	// controller so cloudflared disconnects, then delete the tunnel.
	tunnel, ok, err := ResolveTunnel(ctx, r)
	if err != nil {
		return res, fmt.Errorf("resolving tunnel: %w", err)
	}
	if !ok {
		return res, nil
	}
	if res.StoppedTunnelWorkloads, err = k.StopTunnelController(ctx); err != nil {
		return res, fmt.Errorf("stopping tunnel controller: %w", err)
	}
	if res.TunnelDeleted, err = cfapi.DeleteTunnelByName(ctx, tunnel.AccountID, tunnel.Name); err != nil {
		return res, fmt.Errorf("deleting cloudflare tunnel %q: %w", tunnel.Name, err)
	}
	return res, nil
}

// reapEdgeCert deletes the cluster's edge certificate, if it had one. It reports
// failure as a string rather than an error so a permission or API problem here
// never blocks destroying the cluster (see Result.EdgeCertError).
func reapEdgeCert(ctx context.Context, r ClusterReader, api edgecert.API, zone string) (int, string) {
	if api == nil {
		return 0, ""
	}
	base, err := r.ConfigMapValue(ctx, clusterVarsNamespace, fluxcore.ClusterVarsName, fluxcore.VarBaseDomain)
	if err != nil {
		return 0, fmt.Sprintf("reading %s: %v", fluxcore.VarBaseDomain, err)
	}
	if base == "" || !edgecert.Required(base, zone) {
		return 0, ""
	}
	n := 0
	err = retry.Do(ctx, readTimeout, readDelay, func() (err error) {
		n, err = edgecert.Delete(ctx, api, zone, base)
		return err
	})
	if err != nil {
		return n, err.Error()
	}
	return n, ""
}
