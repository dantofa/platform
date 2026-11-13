// Package edgecert holds the framework-free logic for the per-cluster Cloudflare
// edge certificate that lets a cluster publish dedicated hostnames
// (<app>.<base_domain>). Cloudflare Universal SSL covers the zone apex and one
// wildcard level only, so on a preview (base domain <cluster>.<suffix>) every
// dedicated hostname sits two levels deeper and fails TLS at the edge. The
// platform therefore orders an Advanced Certificate for {*.<base_domain>,
// <base_domain>} at bootstrap and deletes it at teardown.
//
// A cluster's certificate is recognised by its exact host list, never by a
// recorded ID, so re-runs, --force deletes and a cluster whose state is gone all
// find (or avoid) the same pack. It imports neither cobra nor a client SDK; the
// cloudflare adapter satisfies API.
package edgecert

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dantofa/platform/internal/core/retry"
)

// Cloudflare edge-certificate budget. The per-zone limit is unconfirmed for this
// plan, so the Enterprise figure is assumed; OrderCeiling leaves headroom for the
// standing certificates and for leaks, and ordering is refused at or above it.
const (
	ZoneLimit    = 100
	OrderCeiling = 80
)

// Pack statuses the wait loop acts on. Cloudflare reports many intermediate
// states (initializing, pending_validation, pending_issuance,
// pending_deployment, ...); anything not listed here keeps the wait going.
const (
	StatusActive = "active"
	// A *_timed_out status (validation, issuance, deployment) is terminal: the
	// pack will not become active by waiting longer.
	statusTimedOutSuffix = "_timed_out"
)

// terminalStatuses are the other states a pack never leaves for active.
var terminalStatuses = []string{"deleted", "expired", "pending_deletion", "deactivating", "inactive"}

// Pack is a Cloudflare certificate pack as the platform sees it.
type Pack struct {
	ID     string   `json:"id"`
	Type   string   `json:"type,omitempty"`
	Status string   `json:"status"`
	Hosts  []string `json:"hosts"`
}

// API is the Cloudflare certificate surface (satisfied by clients/cloudflare).
type API interface {
	// ListCertificatePacks returns every certificate pack on the zone, in any
	// status. It needs the token's SSL and Certificates read permission.
	ListCertificatePacks(ctx context.Context, zone string) ([]Pack, error)
	// OrderCertificatePack orders an Advanced certificate for hosts.
	OrderCertificatePack(ctx context.Context, zone string, hosts []string) (Pack, error)
	// GetCertificatePack returns one pack by id.
	GetCertificatePack(ctx context.Context, zone, id string) (Pack, error)
	// DeleteCertificatePack deletes one pack by id.
	DeleteCertificatePack(ctx context.Context, zone, id string) error
}

// Hosts returns the host list of a cluster's certificate: the wildcard for its
// dedicated hostnames, plus the base domain itself (a wildcard does not cover its
// own parent).
func Hosts(baseDomain string) []string {
	b := normalize(baseDomain)
	return []string{"*." + b, b}
}

// Required reports whether a cluster with this base domain needs its own edge
// certificate: true when the base domain is strictly below the zone apex, because
// its dedicated hostnames then sit two or more levels under the apex, outside
// Universal SSL. A base domain equal to the apex (production) is covered.
func Required(baseDomain, zone string) bool {
	b, z := normalize(baseDomain), normalize(zone)
	return b != z && strings.HasSuffix(b, "."+z)
}

// Owns reports whether pack is exactly the certificate Hosts(baseDomain) names.
// The match is on the whole host set, so a shared wildcard such as
// *.preview.example.dev, or another cluster's certificate, is never owned.
func Owns(p Pack, baseDomain string) bool {
	return sameHosts(p.Hosts, Hosts(baseDomain))
}

// Find returns the cluster's certificate among packs, preferring one that is not
// already on its way out.
func Find(packs []Pack, baseDomain string) (Pack, bool) {
	var found Pack
	ok := false
	for _, p := range packs {
		if !Owns(p, baseDomain) || isTerminal(p.Status) {
			continue
		}
		if p.Status == StatusActive {
			return p, true
		}
		found, ok = p, true
	}
	return found, ok
}

// CeilingError is returned when the zone already holds too many certificates to
// order another safely.
type CeilingError struct{ Count, Ceiling int }

func (e *CeilingError) Error() string {
	return fmt.Sprintf("the zone already has %d edge certificates (ordering stops at %d of an assumed limit of %d); "+
		"run `dctl digitalocean cluster audit` to find leaked ones and delete them",
		e.Count, e.Ceiling, ZoneLimit)
}

// Request makes sure an edge certificate for baseDomain exists, ordering one when
// it does not, and returns without waiting for issuance (which takes minutes, so
// a caller can do other work first and then call Wait). It is idempotent: an
// existing certificate for the same hosts is reused, and a duplicate-order
// failure from a racing run resolves to the pack that run created. The first
// call is also the permission probe, so a token without SSL access fails here,
// before anything else is built.
func Request(ctx context.Context, api API, zone, baseDomain string) (Pack, error) {
	packs, err := api.ListCertificatePacks(ctx, zone)
	if err != nil {
		return Pack{}, fmt.Errorf("listing edge certificates (the Cloudflare token needs SSL and Certificates edit permission): %w", err)
	}
	if p, ok := Find(packs, baseDomain); ok {
		return p, nil
	}
	if len(packs) >= OrderCeiling {
		return Pack{}, &CeilingError{Count: len(packs), Ceiling: OrderCeiling}
	}
	p, err := api.OrderCertificatePack(ctx, zone, Hosts(baseDomain))
	if err != nil {
		// A racing push may have ordered it between our list and our order.
		if again, lerr := api.ListCertificatePacks(ctx, zone); lerr == nil {
			if p, ok := Find(again, baseDomain); ok {
				return p, nil
			}
		}
		return Pack{}, fmt.Errorf("ordering edge certificate for %s: %w", baseDomain, err)
	}
	return p, nil
}

// Wait polls the pack until it is active, failing on a terminal status or when
// timeout elapses. Transport errors while polling are retried; a definitive API
// error is returned at once.
func Wait(ctx context.Context, api API, zone string, p Pack, timeout, interval time.Duration) (Pack, error) {
	deadline := time.Now().Add(timeout)
	for {
		if p.Status == StatusActive {
			return p, nil
		}
		if isTerminal(p.Status) {
			return p, fmt.Errorf("edge certificate %s for %v ended in status %q", p.ID, p.Hosts, p.Status)
		}
		if !time.Now().Before(deadline) {
			return p, fmt.Errorf("edge certificate %s for %v is still %q after %s", p.ID, p.Hosts, p.Status, timeout)
		}
		select {
		case <-ctx.Done():
			return p, ctx.Err()
		case <-time.After(interval):
		}
		var latest Pack
		err := retry.Do(ctx, timeout, interval, func() (err error) {
			latest, err = api.GetCertificatePack(ctx, zone, p.ID)
			return err
		})
		if err != nil {
			return p, fmt.Errorf("polling edge certificate %s: %w", p.ID, err)
		}
		p = latest
	}
}

// Verify asserts that the edge currently holds an active certificate for
// baseDomain's hosts. It is the readiness gate behind `cluster verify health`: a
// certificate that was never ordered, was deleted, or has fallen out of active
// (a failed renewal, say) fails here as a platform problem instead of surfacing
// later as a downstream TLS timeout. It checks Cloudflare's record of the pack,
// not a TLS handshake.
func Verify(ctx context.Context, api API, zone, baseDomain string) (Pack, error) {
	packs, err := api.ListCertificatePacks(ctx, zone)
	if err != nil {
		return Pack{}, fmt.Errorf("listing edge certificates (the Cloudflare token needs SSL and Certificates read permission): %w", err)
	}
	p, ok := Find(packs, baseDomain)
	if !ok {
		return Pack{}, fmt.Errorf("no edge certificate for %v in zone %s: dedicated hostnames under %s will fail TLS at the edge",
			Hosts(baseDomain), zone, baseDomain)
	}
	if p.Status != StatusActive {
		return p, fmt.Errorf("edge certificate %s for %v is %q, not active", p.ID, p.Hosts, p.Status)
	}
	return p, nil
}

// Delete removes every certificate that exactly matches baseDomain's hosts and
// returns how many it deleted. Idempotent: none left is a success.
func Delete(ctx context.Context, api API, zone, baseDomain string) (int, error) {
	packs, err := api.ListCertificatePacks(ctx, zone)
	if err != nil {
		return 0, fmt.Errorf("listing edge certificates: %w", err)
	}
	n := 0
	var errs []error
	for _, p := range packs {
		if !Owns(p, baseDomain) || p.Status == "deleted" {
			continue
		}
		if err := api.DeleteCertificatePack(ctx, zone, p.ID); err != nil {
			errs = append(errs, fmt.Errorf("deleting edge certificate %s: %w", p.ID, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// Orphans returns the per-cluster certificates in packs whose cluster is gone.
// A pack is a cluster's own when its hosts are exactly {*.B, B} and B is at least
// two labels below the zone apex with a leading label naming the cluster
// (B = <cluster>.<suffix>). The shared wildcard one level below the apex
// (*.preview.example.dev, B one label below the apex) is therefore never
// reported. It is an orphan when no live cluster carries that name.
func Orphans(packs []Pack, zone string, liveClusters []string) []Pack {
	var out []Pack
	for _, p := range packs {
		if isTerminal(p.Status) {
			continue
		}
		cluster, ok := ownerCluster(p, zone)
		if ok && !slices.Contains(liveClusters, cluster) {
			out = append(out, p)
		}
	}
	return out
}

// ownerCluster names the cluster a per-cluster pack belongs to.
func ownerCluster(p Pack, zone string) (string, bool) {
	if len(p.Hosts) != 2 {
		return "", false
	}
	var base string
	for _, h := range p.Hosts {
		if rest, ok := strings.CutPrefix(normalize(h), "*."); ok {
			base = rest
		}
	}
	if base == "" || !Owns(p, base) || !Required(base, zone) {
		return "", false
	}
	cluster, suffix, _ := strings.Cut(base, ".")
	// B one label below the apex is a shared wildcard, not a cluster's.
	if normalize(suffix) == normalize(zone) {
		return "", false
	}
	return cluster, true
}

func isTerminal(status string) bool {
	return strings.HasSuffix(status, statusTimedOutSuffix) || slices.Contains(terminalStatuses, status)
}

func sameHosts(a, b []string) bool {
	na, nb := normalizeAll(a), normalizeAll(b)
	return slices.Equal(na, nb)
}

func normalizeAll(hosts []string) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = normalize(h)
	}
	slices.Sort(out)
	return out
}

func normalize(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
