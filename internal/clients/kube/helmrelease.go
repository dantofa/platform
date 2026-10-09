package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	fluxcore "github.com/dantofa/platform/internal/core/flux"
)

var _ fluxcore.HelmReleaseStatuser = (*Client)(nil)

var helmReleaseGVR = schema.GroupVersionResource{
	Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases",
}

// recentWarningEventLimit bounds how many Warning events HelmReleaseStatuses
// attaches to a terminal release -- enough to show the underlying cause
// without dumping a namespace's whole event history.
const recentWarningEventLimit = 5

// terminalReason reports whether a Ready condition's reason names a release
// that failed outright (InstallFailed, UpgradeFailed, UninstallFailed,
// ArtifactFailed, ...) rather than one still progressing (Progressing,
// ProgressingWithRetry) or waiting on a dependency (DependencyNotReady).
// Flux's helm-controller names every terminal-failure reason with this
// suffix by convention; an empty reason (no Ready condition yet) is not
// terminal.
func terminalReason(reason string) bool {
	return reason != "" && strings.HasSuffix(reason, "Failed")
}

// HelmReleaseStatuses lists the Flux HelmReleases (all namespaces when
// namespace is empty) and reports which have failed terminally. Reason and
// Message come straight from the release's own Ready condition; Events
// carries its namespace's recent Warning events when terminal, since that is
// often the only place the underlying cause (e.g. an admission webhook
// refusal) is recorded. An absent CRD yields an empty list, not an error.
// Implements fluxcore.HelmReleaseStatuser.
func (c *Client) HelmReleaseStatuses(ctx context.Context, namespace string) ([]fluxcore.HelmReleaseStatus, error) {
	var ri dynamic.ResourceInterface = c.dyn.Resource(helmReleaseGVR)
	if namespace != "" {
		ri = c.dyn.Resource(helmReleaseGVR).Namespace(namespace)
	}
	list, err := ri.List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return []fluxcore.HelmReleaseStatus{}, nil
		}
		return nil, err
	}
	out := make([]fluxcore.HelmReleaseStatus, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		reason, message, _ := condition(item, "Ready")
		hs := fluxcore.HelmReleaseStatus{
			Namespace: item.GetNamespace(), Name: item.GetName(),
			Reason: reason, Message: message, Terminal: terminalReason(reason),
		}
		if hs.Terminal {
			hs.Events = c.recentWarningEvents(ctx, hs.Namespace)
		}
		out = append(out, hs)
	}
	return out, nil
}

// recentWarningEvents returns up to recentWarningEventLimit of the
// namespace's most recent Warning events as "reason: message" lines, newest
// first. Best-effort: a read error yields nil rather than failing the
// caller, which has already decided a release is terminal and must not lose
// that verdict over a diagnostics side-read.
func (c *Client) recentWarningEvents(ctx context.Context, namespace string) []string {
	list, err := c.cs.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: "type=Warning"})
	if err != nil || len(list.Items) == 0 {
		return nil
	}
	events := list.Items
	sort.Slice(events, func(i, j int) bool {
		return events[i].LastTimestamp.After(events[j].LastTimestamp.Time)
	})
	if len(events) > recentWarningEventLimit {
		events = events[:recentWarningEventLimit]
	}
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, fmt.Sprintf("%s: %s", e.Reason, e.Message))
	}
	return out
}
