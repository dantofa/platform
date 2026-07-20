package teardown

import (
	"context"
	"errors"
	"fmt"
	"time"

	fluxcore "github.com/dantofa/platform/internal/core/flux"
)

// clusterVarsNamespace is where the cluster-vars ConfigMap lives (mirrors the
// flux-system namespace bootstrap writes it to).
const clusterVarsNamespace = "flux-system"

// cloudflareTokenKey is the key holding the API token in the cloudflare-api
// secret (both the DOKS external-dns and the local tunnel stacks use it).
const cloudflareTokenKey = "api_token"

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

// ResolveCloudflareToken returns the Cloudflare API token from the in-cluster
// cloudflare-api secret (the same one ESO syncs for the ingress controller), so
// teardown reuses the cluster's own scoped credential rather than a flag.
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
	return "", errors.New("no cloudflare-api token found in the cluster (looked in the " +
		"external-dns and cloudflare-tunnel-system namespaces); is the ingress stack bootstrapped?")
}

// ResolveZone returns the cluster's Cloudflare zone apex from the cluster-vars
// ConfigMap (the dns_zone bootstrap derived from base_domain).
func ResolveZone(ctx context.Context, r ClusterReader) (string, error) {
	zone, err := r.ConfigMapValue(ctx, clusterVarsNamespace, fluxcore.ClusterVarsName, fluxcore.VarDNSZone)
	if err != nil {
		return "", err
	}
	if zone == "" {
		return "", fmt.Errorf("no %s in the %s ConfigMap; cannot determine the DNS zone to drain",
			fluxcore.VarDNSZone, fluxcore.ClusterVarsName)
	}
	return zone, nil
}

// Run is the full teardown entrypoint a delete command calls: it resolves the
// Cloudflare token and zone from the cluster, builds the DNS client via newDNS,
// and runs Teardown. Keeping this in core lets both the DOKS and local delete
// commands stay thin and share the exact same flow.
func Run(ctx context.Context, r ClusterReader, k KubeAPI, newDNS func(token string) (DNSAPI, error), timeout, interval time.Duration) (Result, error) {
	token, err := ResolveCloudflareToken(ctx, r)
	if err != nil {
		return Result{}, err
	}
	zone, err := ResolveZone(ctx, r)
	if err != nil {
		return Result{}, err
	}
	dns, err := newDNS(token)
	if err != nil {
		return Result{}, fmt.Errorf("building cloudflare client: %w", err)
	}
	return Teardown(ctx, k, dns, Options{Zone: zone, Timeout: timeout, Interval: interval})
}
