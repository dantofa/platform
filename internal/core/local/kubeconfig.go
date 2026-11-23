package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// DefaultKubeconfigPath is where a local cluster's credentials live unless
// $KUBECONFIG or --kubeconfig says otherwise: the project-local file `connect`
// writes and the cluster.just recipes read (${KUBECONFIG:-.kubeconfig}). Never
// ~/.kube/config: a kind cluster merged there replaces the user's current
// context, so a later kubectl silently targets the wrong cluster.
const DefaultKubeconfigPath = ".kubeconfig"

// MultiEntryKubeconfigError is returned when $KUBECONFIG lists several files.
// kubectl merges such a list for reads, but a write needs one target, and
// guessing which entry the user meant is how credentials land in the wrong file.
type MultiEntryKubeconfigError struct{ Value string }

func (e *MultiEntryKubeconfigError) Error() string {
	return fmt.Sprintf("$KUBECONFIG lists several files (%q); pass --kubeconfig or set $KUBECONFIG to a single path", e.Value)
}

// ResolveKubeconfigPath picks the file a local cluster's credentials are written
// to (and cleaned up from): the explicit flag, else $KUBECONFIG (env), else
// DefaultKubeconfigPath. Empty list entries are ignored, as kubectl does.
func ResolveKubeconfigPath(flag, env string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	var entries []string
	for _, e := range filepath.SplitList(env) {
		if e != "" {
			entries = append(entries, e)
		}
	}
	switch len(entries) {
	case 0:
		return DefaultKubeconfigPath, nil
	case 1:
		return entries[0], nil
	default:
		return "", &MultiEntryKubeconfigError{Value: env}
	}
}

// KindContextName is the context (and cluster and user) name kind gives a cluster.
func KindContextName(cluster string) string { return "kind-" + cluster }

// WriteKubeconfig writes the named cluster's kubeconfig to path, owner-only.
func WriteKubeconfig(ctx context.Context, client LocalClusterAPI, name, path string) error {
	kubeconfig, err := GetKubeconfig(ctx, client, name)
	if err != nil {
		return err
	}
	return writeOwnerOnly(path, kubeconfig)
}

// writeOwnerOnly writes content to path with 0600 permissions, refusing to
// follow a symlink at path (so credentials can't be redirected elsewhere).
func writeOwnerOnly(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(content)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// KubeconfigCleanup reports what delete did with the cluster's kubeconfig file.
type KubeconfigCleanup struct {
	Path    string `json:"path"`
	Removed bool   `json:"removed"`
	// Skipped says why the file was left in place; empty when it was removed.
	Skipped string `json:"skipped,omitempty"`
}

// Reasons a kubeconfig is left in place on delete.
const (
	SkipKubeconfigAbsent  = "not present"
	SkipKubeconfigForeign = "not a kubeconfig holding only this cluster's credentials; left in place"
)

type namedEntry struct {
	Name string `json:"name"`
}

type kubeconfigFile struct {
	Clusters []namedEntry `json:"clusters"`
	Contexts []namedEntry `json:"contexts"`
	Users    []namedEntry `json:"users"`
}

// onlyNames reports whether entries is exactly one entry called name.
func onlyNames(entries []namedEntry, name string) bool {
	return len(entries) == 1 && entries[0].Name == name
}

// ownedBy reports whether data is the single-cluster kubeconfig dctl writes
// for cluster: one cluster, one context and one user, all named kind-<cluster>.
// Anything else (a merged global config, a file holding other clusters, an
// unparseable file) is not dctl's to delete.
func ownedBy(data []byte, cluster string) bool {
	var kc kubeconfigFile
	if err := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096).Decode(&kc); err != nil {
		return false
	}
	name := KindContextName(cluster)
	return onlyNames(kc.Clusters, name) && onlyNames(kc.Contexts, name) && onlyNames(kc.Users, name)
}

// RemoveKubeconfig deletes the kubeconfig file create wrote for cluster. It
// only removes a file holding nothing but that cluster's credentials: pointed
// at a shared file (say, $KUBECONFIG=~/.kube/config) it leaves it alone and
// says why, rather than wiping every other context in it. A missing file is a
// no-op, keeping delete idempotent.
func RemoveKubeconfig(path, cluster string) (KubeconfigCleanup, error) {
	res := KubeconfigCleanup{Path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		res.Skipped = SkipKubeconfigAbsent
		return res, nil
	}
	if err != nil {
		return res, err
	}
	if !ownedBy(data, cluster) {
		res.Skipped = SkipKubeconfigForeign
		return res, nil
	}
	if err := os.Remove(path); err != nil {
		return res, err
	}
	res.Removed = true
	return res, nil
}
