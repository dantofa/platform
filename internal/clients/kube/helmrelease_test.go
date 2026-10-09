package kube

import "testing"

func TestTerminalReason(t *testing.T) {
	cases := map[string]bool{
		"":                     false,
		"InstallSucceeded":     false,
		"UpgradeSucceeded":     false,
		"Progressing":          false,
		"ProgressingWithRetry": false,
		"DependencyNotReady":   false,
		"InstallFailed":        true,
		"UpgradeFailed":        true,
		"UninstallFailed":      true,
		"ArtifactFailed":       true,
	}
	for reason, want := range cases {
		if got := terminalReason(reason); got != want {
			t.Errorf("terminalReason(%q) = %v, want %v", reason, got, want)
		}
	}
}
