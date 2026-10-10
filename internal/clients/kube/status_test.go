package kube

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func withConditions(conditions ...map[string]any) *unstructured.Unstructured {
	items := make([]any, len(conditions))
	for i, c := range conditions {
		items[i] = c
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"conditions": items},
	}}
}

func TestConditionFindsNamedType(t *testing.T) {
	obj := withConditions(
		map[string]any{"type": "Released", "reason": "InstallSucceeded", "message": "released"},
		map[string]any{"type": "Ready", "reason": "InstallFailed", "message": "failed post-install: failed calling webhook"},
	)
	reason, message, ok := condition(obj, "Ready")
	if !ok {
		t.Fatal("expected the Ready condition to be found")
	}
	if reason != "InstallFailed" {
		t.Errorf("reason = %q, want InstallFailed", reason)
	}
	if message != "failed post-install: failed calling webhook" {
		t.Errorf("message = %q", message)
	}
}

func TestConditionMissingTypeNotFound(t *testing.T) {
	obj := withConditions(map[string]any{"type": "Released", "reason": "x", "message": "y"})
	if _, _, ok := condition(obj, "Ready"); ok {
		t.Error("expected no Ready condition to be found")
	}
}

func TestConditionNoConditionsAtAll(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	if _, _, ok := condition(obj, "Ready"); ok {
		t.Error("expected not found on an object with no status.conditions")
	}
}
