// Package cloudflare is a thin adapter over the Cloudflare API for the DNS
// operations teardown needs: checking whether an ingress host still has a record
// in a zone, and force-deleting the records for a host. It carries no
// orchestration logic (that lives in core/teardown); the API token is read from
// the in-cluster cloudflare-api secret by the caller.
package cloudflare

import (
	"context"
	"fmt"

	cf "github.com/cloudflare/cloudflare-go"

	teardowncore "github.com/dantofa/platform/internal/core/teardown"
)

var _ teardowncore.DNSAPI = (*Client)(nil)

// Client wraps the Cloudflare API bound to a single account token, caching the
// zone-name -> zone-id lookups it resolves.
type Client struct {
	api    *cf.API
	zoneID map[string]string
}

// New builds a Cloudflare client from an API token (the value of the
// cloudflare-api secret's api_token key).
func New(token string) (*Client, error) {
	api, err := cf.NewWithAPIToken(token)
	if err != nil {
		return nil, err
	}
	return &Client{api: api, zoneID: map[string]string{}}, nil
}

func (c *Client) resolveZone(zone string) (*cf.ResourceContainer, error) {
	id, ok := c.zoneID[zone]
	if !ok {
		var err error
		if id, err = c.api.ZoneIDByName(zone); err != nil {
			return nil, fmt.Errorf("resolving cloudflare zone %q: %w", zone, err)
		}
		c.zoneID[zone] = id
	}
	return cf.ZoneIdentifier(id), nil
}

// RecordedHosts returns the subset of hosts that still have at least one DNS
// record at that exact name in the zone (the resolvable A/CNAME is what leaks, so
// an empty result means the ingress records are gone). Implements
// teardowncore.DNSAPI.
func (c *Client) RecordedHosts(ctx context.Context, zone string, hosts []string) ([]string, error) {
	rc, err := c.resolveZone(zone)
	if err != nil {
		return nil, err
	}
	var still []string
	for _, host := range hosts {
		recs, _, err := c.api.ListDNSRecords(ctx, rc, cf.ListDNSRecordsParams{Name: host})
		if err != nil {
			return nil, fmt.Errorf("listing records for %q: %w", host, err)
		}
		if len(recs) > 0 {
			still = append(still, host)
		}
	}
	return still, nil
}

// DeleteHostRecords deletes every record at each host's exact name in the zone
// (A/CNAME plus any same-name TXT), returning the number of records removed. The
// fallback for when the ingress controller did not drain them itself. Implements
// teardowncore.DNSAPI.
func (c *Client) DeleteHostRecords(ctx context.Context, zone string, hosts []string) (int, error) {
	rc, err := c.resolveZone(zone)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, host := range hosts {
		recs, _, err := c.api.ListDNSRecords(ctx, rc, cf.ListDNSRecordsParams{Name: host})
		if err != nil {
			return n, fmt.Errorf("listing records for %q: %w", host, err)
		}
		for _, rec := range recs {
			if err := c.api.DeleteDNSRecord(ctx, rc, rec.ID); err != nil {
				return n, fmt.Errorf("deleting record %s (%s): %w", rec.Name, rec.ID, err)
			}
			n++
		}
	}
	return n, nil
}
