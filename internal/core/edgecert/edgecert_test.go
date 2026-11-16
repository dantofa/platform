package edgecert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeAPI is an in-memory zone. Packs ordered through it start in `initial` and
// move to active after `activateAfter` polls.
type fakeAPI struct {
	packs         []Pack
	initial       string
	activateAfter int
	polls         int
	orderErr      error
	listErr       error
	getErr        error
	ordered       int
	deleted       []string
}

func (f *fakeAPI) ListCertificatePacks(context.Context, string) ([]Pack, error) {
	return append([]Pack(nil), f.packs...), f.listErr
}

func (f *fakeAPI) OrderCertificatePack(_ context.Context, _ string, hosts []string) (Pack, error) {
	if f.orderErr != nil {
		return Pack{}, f.orderErr
	}
	f.ordered++
	p := Pack{ID: fmt.Sprintf("new-%d", f.ordered), Status: f.initial, Hosts: hosts}
	f.packs = append(f.packs, p)
	return p, nil
}

func (f *fakeAPI) GetCertificatePack(_ context.Context, _, id string) (Pack, error) {
	if f.getErr != nil {
		return Pack{}, f.getErr
	}
	f.polls++
	for i := range f.packs {
		if f.packs[i].ID == id {
			if f.polls >= f.activateAfter {
				f.packs[i].Status = StatusActive
			}
			return f.packs[i], nil
		}
	}
	return Pack{}, errors.New("not found")
}

func (f *fakeAPI) DeleteCertificatePack(_ context.Context, _, id string) error {
	f.deleted = append(f.deleted, id)
	f.packs = slices.DeleteFunc(f.packs, func(p Pack) bool { return p.ID == id })
	return nil
}

const (
	zone = "dantofa.dev"
	base = "auth-pr42.preview.dantofa.dev"
)

func TestHosts(t *testing.T) {
	got := strings.Join(Hosts("Auth-PR42.preview.dantofa.dev."), ",")
	if want := "*.auth-pr42.preview.dantofa.dev,auth-pr42.preview.dantofa.dev"; got != want {
		t.Errorf("Hosts = %q, want %q", got, want)
	}
}

func TestRequired(t *testing.T) {
	for _, tc := range []struct {
		base, zone string
		want       bool
	}{
		{"auth-pr42.preview.dantofa.dev", "dantofa.dev", true},
		{"preview.dantofa.dev", "dantofa.dev", true}, // apps at <app>.preview.… are two levels deep
		{"dantofa.dev", "dantofa.dev", false},        // production at the apex: Universal SSL covers it
		{"dantofa.dev.", "dantofa.dev", false},
		{"other.com", "dantofa.dev", false}, // not in the zone at all
		{"xdantofa.dev", "dantofa.dev", false},
	} {
		if got := Required(tc.base, tc.zone); got != tc.want {
			t.Errorf("Required(%q, %q) = %v, want %v", tc.base, tc.zone, got, tc.want)
		}
	}
}

func TestOwnsRequiresExactHostSet(t *testing.T) {
	own := Pack{Hosts: []string{base, "*." + base}}
	if !Owns(own, base) {
		t.Error("a pack with exactly the cluster's hosts (any order) must be owned")
	}
	for name, p := range map[string]Pack{
		"shared wildcard":   {Hosts: []string{"*.preview.dantofa.dev", "preview.dantofa.dev"}},
		"another PR":        {Hosts: Hosts("auth-pr43.preview.dantofa.dev")},
		"wildcard only":     {Hosts: []string{"*." + base}},
		"extra host":        {Hosts: append(Hosts(base), "extra.example.dev")},
		"universal default": {Hosts: []string{"dantofa.dev", "*.dantofa.dev"}},
	} {
		if Owns(p, base) {
			t.Errorf("%s must not be owned by %s", name, base)
		}
	}
}

func TestRequestReusesExistingCertificate(t *testing.T) {
	api := &fakeAPI{packs: []Pack{{ID: "mine", Status: StatusActive, Hosts: Hosts(base)}}}
	p, err := Request(context.Background(), api, zone, base)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if p.ID != "mine" || api.ordered != 0 {
		t.Errorf("expected reuse of the existing pack, got %+v (ordered %d)", p, api.ordered)
	}
}

func TestRequestOrdersWhenAbsentAndIgnoresSharedWildcard(t *testing.T) {
	api := &fakeAPI{
		initial: "initializing",
		packs:   []Pack{{ID: "shared", Status: StatusActive, Hosts: Hosts("preview.dantofa.dev")}},
	}
	p, err := Request(context.Background(), api, zone, base)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if api.ordered != 1 || p.ID != "new-1" {
		t.Errorf("expected exactly one new order, got %+v (ordered %d)", p, api.ordered)
	}
}

func TestRequestReplacesAnExpiredPack(t *testing.T) {
	api := &fakeAPI{initial: "initializing", packs: []Pack{{ID: "old", Status: "validation_timed_out", Hosts: Hosts(base)}}}
	if _, err := Request(context.Background(), api, zone, base); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if api.ordered != 1 {
		t.Error("a timed-out pack must not be reused")
	}
}

func TestRequestRefusesAtCeiling(t *testing.T) {
	api := &fakeAPI{}
	for i := range OrderCeiling {
		api.packs = append(api.packs, Pack{ID: fmt.Sprint(i), Status: StatusActive, Hosts: []string{fmt.Sprintf("h%d.example.dev", i)}})
	}
	_, err := Request(context.Background(), api, zone, base)
	var ce *CeilingError
	if !errors.As(err, &ce) {
		t.Fatalf("expected CeilingError, got %v", err)
	}
	if api.ordered != 0 {
		t.Error("must not order at the ceiling")
	}
}

func TestRequestCeilingDoesNotBlockReuse(t *testing.T) {
	api := &fakeAPI{packs: []Pack{{ID: "mine", Status: StatusActive, Hosts: Hosts(base)}}}
	for i := range OrderCeiling {
		api.packs = append(api.packs, Pack{ID: fmt.Sprint(i), Status: StatusActive, Hosts: []string{fmt.Sprintf("h%d.example.dev", i)}})
	}
	if _, err := Request(context.Background(), api, zone, base); err != nil {
		t.Errorf("an existing certificate must be reusable at the ceiling: %v", err)
	}
}

func TestRequestFailsFastOnListError(t *testing.T) {
	api := &fakeAPI{listErr: errors.New("403 forbidden")}
	_, err := Request(context.Background(), api, zone, base)
	if err == nil || !strings.Contains(err.Error(), "SSL and Certificates") {
		t.Errorf("expected a permission-hinting error, got %v", err)
	}
}

func TestRequestResolvesRacingOrder(t *testing.T) {
	// The order fails as a duplicate, but a concurrent run's pack is now listed.
	api := &racingAPI{fakeAPI: fakeAPI{orderErr: errors.New("duplicate")}}
	p, err := Request(context.Background(), api, zone, base)
	if err != nil || p.ID != "raced" {
		t.Errorf("expected to adopt the racing run's pack, got %+v, %v", p, err)
	}
}

// racingAPI lists no packs on the first call and the racing run's pack after.
type racingAPI struct {
	fakeAPI
	listed int
}

func (r *racingAPI) ListCertificatePacks(context.Context, string) ([]Pack, error) {
	r.listed++
	if r.listed == 1 {
		return nil, nil
	}
	return []Pack{{ID: "raced", Status: "initializing", Hosts: Hosts(base)}}, nil
}

func TestRequestSurfacesOrderError(t *testing.T) {
	api := &fakeAPI{orderErr: errors.New("boom")}
	if _, err := Request(context.Background(), api, zone, base); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected the order error, got %v", err)
	}
}

func TestWaitReturnsOnceActive(t *testing.T) {
	api := &fakeAPI{initial: "pending_validation", activateAfter: 3}
	p, err := Request(context.Background(), api, zone, base)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Wait(context.Background(), api, zone, p, time.Second, time.Millisecond)
	if err != nil || got.Status != StatusActive {
		t.Errorf("Wait = %+v, %v", got, err)
	}
}

func TestWaitFailsOnTerminalStatus(t *testing.T) {
	api := &fakeAPI{packs: []Pack{{ID: "x", Status: "validation_timed_out", Hosts: Hosts(base)}}}
	_, err := Wait(context.Background(), api, zone, api.packs[0], time.Second, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "validation_timed_out") {
		t.Errorf("expected terminal-status error, got %v", err)
	}
}

func TestWaitTimesOut(t *testing.T) {
	api := &fakeAPI{initial: "pending_validation", activateAfter: 1 << 30}
	p, _ := Request(context.Background(), api, zone, base)
	_, err := Wait(context.Background(), api, zone, p, 20*time.Millisecond, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still") {
		t.Errorf("expected timeout error, got %v", err)
	}
}

func TestWaitDoesNotRetryDefinitiveErrors(t *testing.T) {
	api := &fakeAPI{initial: "pending_validation", getErr: errors.New("403")}
	p, _ := Request(context.Background(), api, zone, base)
	start := time.Now()
	if _, err := Wait(context.Background(), api, zone, p, time.Minute, time.Millisecond); err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("a definitive error must fail fast, not burn the timeout")
	}
}

func TestWaitRidesOutDroppedConnection(t *testing.T) {
	api := &flakyGet{fakeAPI: fakeAPI{initial: "pending_validation", activateAfter: 1}}
	p, _ := Request(context.Background(), api, zone, base)
	if _, err := Wait(context.Background(), api, zone, p, time.Second, time.Millisecond); err != nil {
		t.Errorf("a transient drop must be retried: %v", err)
	}
}

type flakyGet struct {
	fakeAPI
	failed bool
}

func (f *flakyGet) GetCertificatePack(ctx context.Context, z, id string) (Pack, error) {
	if !f.failed {
		f.failed = true
		return Pack{}, io.ErrUnexpectedEOF
	}
	return f.fakeAPI.GetCertificatePack(ctx, z, id)
}

func TestDeleteOnlyRemovesTheClustersOwnPack(t *testing.T) {
	api := &fakeAPI{packs: []Pack{
		{ID: "shared", Status: StatusActive, Hosts: Hosts("preview.dantofa.dev")},
		{ID: "other-pr", Status: StatusActive, Hosts: Hosts("auth-pr43.preview.dantofa.dev")},
		{ID: "mine", Status: StatusActive, Hosts: Hosts(base)},
	}}
	n, err := Delete(context.Background(), api, zone, base)
	if err != nil || n != 1 {
		t.Fatalf("Delete = %d, %v", n, err)
	}
	if len(api.deleted) != 1 || api.deleted[0] != "mine" {
		t.Errorf("deleted %v, want only [mine]", api.deleted)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	api := &fakeAPI{}
	if n, err := Delete(context.Background(), api, zone, base); err != nil || n != 0 {
		t.Errorf("Delete on nothing = %d, %v", n, err)
	}
}

func TestOrphans(t *testing.T) {
	packs := []Pack{
		{ID: "shared", Status: StatusActive, Hosts: Hosts("preview.dantofa.dev")},
		{ID: "live", Status: StatusActive, Hosts: Hosts("auth-pr1.preview.dantofa.dev")},
		{ID: "gone", Status: StatusActive, Hosts: Hosts("auth-pr2.preview.dantofa.dev")},
		{ID: "gone-pending", Status: "pending_validation", Hosts: Hosts("www-pr9.preview.dantofa.dev")},
		{ID: "deleted", Status: "deleted", Hosts: Hosts("auth-pr3.preview.dantofa.dev")},
		{ID: "universal", Status: StatusActive, Hosts: []string{"dantofa.dev", "*.dantofa.dev"}},
		{ID: "three-hosts", Status: StatusActive, Hosts: append(Hosts("auth-pr4.preview.dantofa.dev"), "x.dantofa.dev")},
	}
	got := Orphans(packs, zone, []string{"auth-pr1"})
	var ids []string
	for _, p := range got {
		ids = append(ids, p.ID)
	}
	if want := "gone,gone-pending"; strings.Join(ids, ",") != want {
		t.Errorf("orphans = %v, want %s (the shared wildcard and foreign packs must never be reported)", ids, want)
	}
}

func TestVerify(t *testing.T) {
	for _, tc := range []struct {
		name    string
		packs   []Pack
		listErr error
		wantErr string // substring; "" means success
	}{
		{"active", []Pack{{ID: "mine", Status: StatusActive, Hosts: Hosts(base)}}, nil, ""},
		{"never ordered", nil, nil, "no edge certificate"},
		{"only the shared wildcard", []Pack{{ID: "shared", Status: StatusActive, Hosts: Hosts("preview.dantofa.dev")}}, nil, "no edge certificate"},
		{"another PR's", []Pack{{ID: "o", Status: StatusActive, Hosts: Hosts("auth-pr43.preview.dantofa.dev")}}, nil, "no edge certificate"},
		{"still issuing", []Pack{{ID: "mine", Status: "pending_validation", Hosts: Hosts(base)}}, nil, "not active"},
		{"timed out", []Pack{{ID: "mine", Status: "validation_timed_out", Hosts: Hosts(base)}}, nil, "no edge certificate"},
		{"deleted", []Pack{{ID: "mine", Status: "deleted", Hosts: Hosts(base)}}, nil, "no edge certificate"},
		{"no permission", nil, errors.New("403"), "SSL and Certificates"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Verify(context.Background(), &fakeAPI{packs: tc.packs, listErr: tc.listErr}, zone, base)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.wantErr == "" && p.ID != "mine":
				t.Errorf("returned %+v", p)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestLifecycle runs the whole cycle the CI drill drives against the real API:
// request -> wait -> verify -> delete -> verify fails -> delete again is a no-op.
func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	api := &fakeAPI{initial: "pending_validation", activateAfter: 2}
	if _, err := Verify(ctx, api, zone, base); err == nil {
		t.Fatal("verify must fail before the certificate exists")
	}
	p, err := Request(ctx, api, zone, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(ctx, api, zone, base); err == nil {
		t.Fatal("verify must fail while the certificate is still issuing")
	}
	if _, err := Wait(ctx, api, zone, p, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(ctx, api, zone, base); err != nil {
		t.Fatalf("verify must pass once active: %v", err)
	}
	if n, err := Delete(ctx, api, zone, base); err != nil || n != 1 {
		t.Fatalf("Delete = %d, %v", n, err)
	}
	if _, err := Verify(ctx, api, zone, base); err == nil {
		t.Fatal("verify must fail once the certificate is deleted")
	}
	if n, err := Delete(ctx, api, zone, base); err != nil || n != 0 {
		t.Fatalf("a second Delete must be a no-op, got %d, %v", n, err)
	}
}
