// Package teardown holds the framework-free orchestration for gracefully
// draining a cluster's ingress DNS before the cluster is destroyed. Destroying a
// cluster outright orphans the Cloudflare records (and, on local, the tunnel)
// its ingress controller created, because the controller is killed before it can
// reconcile their removal. Teardown instead deletes the Ingress objects while the
// controller is still alive (so external-dns / the tunnel controller delete the
// records themselves), suspends Flux so nothing re-creates them, then waits for
// the records to actually disappear from Cloudflare — API-deleting any stragglers
// as a fallback. It imports neither cobra nor a client SDK; the kube and
// cloudflare adapters satisfy its interfaces.
package teardown

import (
	"context"
	"fmt"
	"time"
)

// KubeAPI is the cluster-side surface teardown drives (satisfied by clients/kube).
type KubeAPI interface {
	// IngressHosts returns every hostname declared across all Ingresses.
	IngressHosts(ctx context.Context) ([]string, error)
	// SuspendKustomizations sets spec.suspend on every Flux Kustomization so it
	// stops reconciling (and cannot re-create the Ingresses). Returns the count.
	SuspendKustomizations(ctx context.Context) (int, error)
	// DeleteIngresses deletes every Ingress in all namespaces. Returns the count.
	DeleteIngresses(ctx context.Context) (int, error)
	// StopTunnelController deletes the Cloudflare Tunnel controller workloads so
	// cloudflared disconnects (a precondition for deleting the tunnel object).
	// A no-op returning 0 when the controller is absent (e.g. DOKS). Returns the
	// count deleted.
	StopTunnelController(ctx context.Context) (int, error)
}

// DNSAPI is the Cloudflare record surface teardown polls and falls back on
// (satisfied by clients/cloudflare).
type DNSAPI interface {
	// RecordedHosts returns the subset of hosts that still have a DNS record in
	// the zone (so an empty result means the drain is complete).
	RecordedHosts(ctx context.Context, zone string, hosts []string) ([]string, error)
	// DeleteHostRecords force-deletes any records for hosts in the zone (the
	// fallback when the controller did not drain them in time). Returns the count.
	DeleteHostRecords(ctx context.Context, zone string, hosts []string) (int, error)
}

// CloudflareAPI is the full Cloudflare surface: DNS record ops plus account-level
// tunnel deletion (local clusters whose tunnel controller leaves its Cloudflare
// Tunnel behind on teardown). Satisfied by clients/cloudflare.
type CloudflareAPI interface {
	DNSAPI
	// DeleteTunnelByName deletes the tunnel(s) named name in the account, returning
	// whether any were deleted.
	DeleteTunnelByName(ctx context.Context, accountID, name string) (bool, error)
}

// Options parameterizes a teardown run.
type Options struct {
	Zone     string        // the Cloudflare zone apex (dns_zone) records live in
	Timeout  time.Duration // max time to wait for the controller to drain records
	Interval time.Duration // poll interval while waiting
}

// Result reports what teardown did, for rendering.
type Result struct {
	Hosts                   []string `json:"hosts"`
	SuspendedKustomizations int      `json:"suspended_kustomizations"`
	DeletedIngresses        int      `json:"deleted_ingresses"`
	ForceDeletedRecords     int      `json:"force_deleted_records"`
	Drained                 bool     `json:"drained"`
	StoppedTunnelWorkloads  int      `json:"stopped_tunnel_workloads,omitempty"`
	TunnelDeleted           bool     `json:"tunnel_deleted,omitempty"`
	// Skipped is set when the cluster has no ingress stack (no cloudflare-api
	// secret), so there was nothing to tear down.
	Skipped bool `json:"skipped,omitempty"`
}

// Teardown runs the graceful drain. It is a no-op success when the cluster has no
// Ingresses (nothing to leak). Otherwise it suspends Flux, deletes the Ingresses,
// waits for Cloudflare to reflect the removal, and — if the controller did not
// finish in time — force-deletes the stragglers via the API. It returns an error
// only if records for the ingress hosts still remain after that fallback.
func Teardown(ctx context.Context, k KubeAPI, dns DNSAPI, opts Options) (Result, error) {
	var res Result

	hosts, err := k.IngressHosts(ctx)
	if err != nil {
		return res, fmt.Errorf("listing ingress hosts: %w", err)
	}
	res.Hosts = hosts
	if len(hosts) == 0 {
		// No ingress, so no controller-managed records to drain.
		res.Drained = true
		return res, nil
	}

	// Stop Flux first so it cannot re-apply the Ingresses we are about to delete.
	if res.SuspendedKustomizations, err = k.SuspendKustomizations(ctx); err != nil {
		return res, fmt.Errorf("suspending kustomizations: %w", err)
	}
	// Delete the Ingresses while the controller is alive so it removes the records.
	if res.DeletedIngresses, err = k.DeleteIngresses(ctx); err != nil {
		return res, fmt.Errorf("deleting ingresses: %w", err)
	}

	// Wait for the controller to reflect the removal in Cloudflare.
	remaining, err := waitForDrain(ctx, dns, opts, hosts)
	if err != nil {
		return res, err
	}
	if len(remaining) == 0 {
		res.Drained = true
		return res, nil
	}

	// Fallback: the controller did not drain in time (e.g. it was already gone).
	// Delete the straggler records directly.
	n, err := dns.DeleteHostRecords(ctx, opts.Zone, remaining)
	if err != nil {
		return res, fmt.Errorf("force-deleting %d straggler record host(s): %w", len(remaining), err)
	}
	res.ForceDeletedRecords = n

	// Confirm the zone is actually clean now.
	still, err := dns.RecordedHosts(ctx, opts.Zone, hosts)
	if err != nil {
		return res, fmt.Errorf("re-checking records after force delete: %w", err)
	}
	if len(still) > 0 {
		return res, fmt.Errorf("dns records still present after teardown for hosts: %v", still)
	}
	res.Drained = true
	return res, nil
}

// waitForDrain polls until no ingress host has a record, the timeout elapses, or
// the context is cancelled. It returns the hosts that still have records (empty
// when fully drained).
func waitForDrain(ctx context.Context, dns DNSAPI, opts Options, hosts []string) ([]string, error) {
	deadline := time.Now().Add(opts.Timeout)
	for {
		remaining, err := dns.RecordedHosts(ctx, opts.Zone, hosts)
		if err != nil {
			return nil, fmt.Errorf("checking cloudflare records: %w", err)
		}
		if len(remaining) == 0 || !time.Now().Before(deadline) {
			return remaining, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(opts.Interval):
		}
	}
}
