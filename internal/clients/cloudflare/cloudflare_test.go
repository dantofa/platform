package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cf "github.com/cloudflare/cloudflare-go"
)

// Cloudflare pages the pack list (20 by default). A zone past one page must
// still be listed whole, or the order ceiling, Find and the orphan audit all
// work from a truncated view; and created_on must come through for the prune's
// grace period.
func TestListCertificatePacksReadsEveryPage(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zones/z1/ssl/certificate_packs" || r.URL.Query().Get("status") != "all" {
			http.NotFound(w, r)
			return
		}
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"success":true,"errors":[],"messages":[],
			"result":[{"id":"p%s","type":"advanced","status":"active","hosts":["a.example.dev"],
			           "created_on":"2026-11-0%sT10:00:00Z"}],
			"result_info":{"page":%s,"per_page":1,"total_pages":2,"count":1,"total_count":2}}`, page, page, page)
	}))
	defer srv.Close()
	api, err := cf.NewWithAPIToken("token", cf.BaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{api: api, zoneID: map[string]string{"example.dev": "z1"}}

	packs, err := c.ListCertificatePacks(context.Background(), "example.dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 2 || packs[0].ID != "p1" || packs[1].ID != "p2" {
		t.Fatalf("got %+v, want both pages", packs)
	}
	if want := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC); !packs[1].CreatedOn.Equal(want) {
		t.Errorf("created_on = %v, want %v", packs[1].CreatedOn, want)
	}
	if fmt.Sprint(pages) != "[1 2]" {
		t.Errorf("requested pages %v, want [1 2]", pages)
	}
}
