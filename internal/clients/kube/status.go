package kube

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/cli-utils/pkg/kstatus/status"

	fluxcore "github.com/dantofa/platform/internal/core/flux"
)

var _ fluxcore.KustomizationStatuser = (*Client)(nil)

var kustomizationGVR = schema.GroupVersionResource{
	Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations",
}

// condition returns the reason/message of the named condition type (e.g.
// "Ready") on an unstructured object's status.conditions, or ("", "", false)
// if absent. kstatus's own Compute synthesizes a generic message from
// generation/observedGeneration (useful the instant a reconciler's write
// hasn't landed yet, but wrong once it has and says nothing about *why* a
// stack is unhealthy); the condition's own message is Flux's actual verdict.
func condition(obj *unstructured.Unstructured, conditionType string) (reason, message string, found bool) {
	conditions, ok, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !ok {
		return "", "", false
	}
	for _, c := range conditions {
		m, ok := c.(map[string]any)
		if !ok || m["type"] != conditionType {
			continue
		}
		reason, _ := m["reason"].(string)
		message, _ := m["message"].(string)
		return reason, message, true
	}
	return "", "", false
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
		// The Ready condition's own message, independent of kstatus's verdict
		// above: kstatus can report a generic "generation is 1 but latest
		// observed generation is -1" the instant a health check starts, while
		// Flux writes the actual failure reason into this condition moments
		// later. Surfacing both means a caller never has to choose one.
		if _, msg, ok := condition(item, "Ready"); ok {
			ks.ConditionMessage = msg
		}
		out = append(out, ks)
	}
	return out, nil
}
