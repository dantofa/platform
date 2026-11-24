package digitalocean

import (
	"errors"
	"testing"

	"github.com/digitalocean/godo"

	core "github.com/dantofa/platform/internal/core/digitalocean"
)

func TestDeleteSelectiveErrorSurfacesUnmanagedResource(t *testing.T) {
	err := deleteSelectiveError(&godo.ErrorResponse{
		Message:   "resource [do:volume:560f9ee6-e82a-11f1-ad12-96ff3ee6960f] specified is not a managed resource",
		RequestID: "req-1",
	})
	var u *core.UnmanagedResourceError
	if !errors.As(err, &u) || u.ID != "560f9ee6-e82a-11f1-ad12-96ff3ee6960f" {
		t.Fatalf("want UnmanagedResourceError for the volume id, got %#v", err)
	}
	var api *APIError
	if !errors.As(err, &api) {
		t.Error("the provider payload must still reach the CLI through APIError")
	}
}

func TestDeleteSelectiveErrorLeavesOtherErrorsAlone(t *testing.T) {
	err := deleteSelectiveError(&godo.ErrorResponse{Message: "unable to authenticate you"})
	var u *core.UnmanagedResourceError
	if errors.As(err, &u) {
		t.Fatalf("an unrelated error must not be treated as unmanaged: %v", err)
	}
	var api *APIError
	if !errors.As(err, &api) {
		t.Errorf("want APIError, got %#v", err)
	}
}
