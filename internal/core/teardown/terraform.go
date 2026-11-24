package teardown

import (
	"context"
	"fmt"
	"time"

	"github.com/dantofa/platform/internal/core/retry"
)

// TerraformAPI is the cluster-side surface the Terraform drain drives (satisfied
// by clients/kube). Both are no-ops returning 0 on a cluster without the
// Terraform CRD.
type TerraformAPI interface {
	// TerraformObjects counts the tofu-controller Terraform objects in every
	// namespace.
	TerraformObjects(ctx context.Context) (int, error)
	// DeleteTerraformObjects requests deletion of every Terraform object, returning
	// the count. Deletion completes asynchronously: tofu-controller's finalizer
	// runs `destroy` first for objects with destroyResourcesOnDeletion.
	DeleteTerraformObjects(ctx context.Context) (int, error)
}

// terraformDrainTimeout bounds how long Run waits for Terraform objects to go.
// Each needs a runner pod (image pull, init, provider download) before its
// destroy even starts, so it is far longer than a DNS drain. A var so tests can
// shorten it.
var terraformDrainTimeout = 10 * time.Minute

// DrainTerraform deletes every Terraform object and waits until all are gone, so
// resources they manage outside the cluster are destroyed while the controller
// and its runners can still reach them. Without it they leak on every delete: the
// state lives in the cluster and dies with it, and nothing runs `destroy`.
//
// suspend stops Flux first so nothing re-applies the objects. It is called only
// when there is something to drain (a cluster without Terraform objects is left
// untouched). Objects without destroyResourcesOnDeletion just drop their state.
// It returns the number deleted, and an error if any remain after timeout (a
// destroy failing, or the controller gone so its finalizer never runs).
func DrainTerraform(ctx context.Context, k TerraformAPI, suspend func(context.Context) error, timeout, interval time.Duration) (int, error) {
	var n int
	if err := retry.Do(ctx, readTimeout, readDelay, func() (err error) {
		n, err = k.TerraformObjects(ctx)
		return err
	}); err != nil {
		return 0, fmt.Errorf("listing terraform objects: %w", err)
	}
	if n == 0 {
		return 0, nil
	}
	if err := suspend(ctx); err != nil {
		return 0, fmt.Errorf("suspending kustomizations: %w", err)
	}
	deleted, err := k.DeleteTerraformObjects(ctx)
	if err != nil {
		return deleted, fmt.Errorf("deleting terraform objects: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		remaining, err := k.TerraformObjects(ctx)
		if err != nil && !retry.Transient(err) {
			return deleted, fmt.Errorf("waiting for terraform objects to be deleted: %w", err)
		}
		if err == nil && remaining == 0 {
			return deleted, nil
		}
		if !time.Now().Before(deadline) {
			return deleted, fmt.Errorf("%d terraform object(s) still present after %s: a destroy is failing or the controller is gone "+
				"(see `kubectl get terraforms -A`)", remaining, timeout)
		}
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		case <-time.After(interval):
		}
	}
}
