package kube

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/cli-utils/pkg/kstatus/status"

	fluxcore "github.com/dantofa/platform/internal/core/flux"
)

var _ fluxcore.KustomizationStatuser = (*Client)(nil)

var kustomizationGVR = schema.GroupVersionResource{
	Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations",
}

// KustomizationStatuses lists the Flux Kustomizations (all namespaces when
// namespace is empty) and computes each one's reconciliation status with
// kstatus. An absent CRD (Flux not installed) yields an empty list, not an
// error. Implements fluxcore.KustomizationStatuser.
func (c *Client) KustomizationStatuses(ctx context.Context, namespace string) ([]fluxcore.KustomizationStatus, error) {
	var ri dynamic.ResourceInterface = c.dyn.Resource(kustomizationGVR)
	if namespace != "" {
		ri = c.dyn.Resource(kustomizationGVR).Namespace(namespace)
	}
	list, err := ri.List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return []fluxcore.KustomizationStatus{}, nil
		}
		return nil, err
	}
	out := make([]fluxcore.KustomizationStatus, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		ks := fluxcore.KustomizationStatus{Namespace: item.GetNamespace(), Name: item.GetName()}
		res, cerr := status.Compute(item)
		if cerr != nil {
			ks.Status = status.UnknownStatus.String()
			ks.Message = cerr.Error()
		} else {
			ks.Status = res.Status.String()
			ks.Message = res.Message
			ks.Ready = res.Status == status.CurrentStatus
		}
		out = append(out, ks)
	}
	return out, nil
}
