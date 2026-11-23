package local

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveKubeconfigPath(t *testing.T) {
	for _, tc := range []struct {
		name, flag, env, want string
	}{
		{"flag wins over env", "custom", "/env/kc", "custom"},
		{"env when no flag", "", "/env/kc", "/env/kc"},
		{"project file when neither", "", "", DefaultKubeconfigPath},
		{"empty list entries ignored", "", ":/env/kc:", "/env/kc"},
		{"flag wins over a multi-entry env", "custom", "/a:/b", "custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveKubeconfigPath(tc.flag, tc.env)
			if err != nil || got != tc.want {
				t.Errorf("ResolveKubeconfigPath(%q, %q) = %q, %v; want %q", tc.flag, tc.env, got, err, tc.want)
			}
		})
	}
}

// A write needs one target; dctl must not guess which entry of a list was meant.
func TestResolveKubeconfigPathRejectsMultiEntry(t *testing.T) {
	_, err := ResolveKubeconfigPath("", "/a:/b")
	var multi *MultiEntryKubeconfigError
	if !errors.As(err, &multi) || multi.Value != "/a:/b" {
		t.Fatalf("want MultiEntryKubeconfigError, got %v", err)
	}
}

// kindKubeconfig is the shape `kind get kubeconfig` emits for one cluster.
func kindKubeconfig(cluster string) string {
	n := "kind-" + cluster
	return `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://127.0.0.1:40000
  name: ` + n + `
contexts:
- context:
    cluster: ` + n + `
    user: ` + n + `
  name: ` + n + `
current-context: ` + n + `
users:
- name: ` + n + `
  user: {}
`
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".kubeconfig")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRemoveKubeconfigRemovesTheClustersOwnFile(t *testing.T) {
	path := writeFile(t, kindKubeconfig("local"))
	res, err := RemoveKubeconfig(path, "local")
	if err != nil || !res.Removed || res.Skipped != "" || res.Path != path {
		t.Fatalf("got %+v err %v", res, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still present: %v", err)
	}
}

// The guard that keeps `KUBECONFIG=~/.kube/config dctl local cluster delete`
// from wiping every other context in the user's global config.
func TestRemoveKubeconfigLeavesSharedFiles(t *testing.T) {
	merged := kindKubeconfig("local") + `- name: do-prod
  user: {}
`
	for name, content := range map[string]string{
		"other user too":     merged,
		"another cluster":    kindKubeconfig("other"),
		"not a kubeconfig":   "{{ not yaml",
		"empty":              "",
		"no clusters at all": "apiVersion: v1\nkind: Config\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, content)
			res, err := RemoveKubeconfig(path, "local")
			if err != nil || res.Removed || res.Skipped != SkipKubeconfigForeign {
				t.Fatalf("got %+v err %v", res, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("file was removed: %v", err)
			}
		})
	}
}

func TestRemoveKubeconfigMissingIsNoOp(t *testing.T) {
	res, err := RemoveKubeconfig(filepath.Join(t.TempDir(), "absent"), "local")
	if err != nil || res.Removed || res.Skipped != SkipKubeconfigAbsent {
		t.Fatalf("got %+v err %v", res, err)
	}
}
