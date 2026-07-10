package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// The registry answers on :5000 inside its container; the host maps a chosen
// port to it, and in-cluster clients reach it by container name over the kind
// network.
const (
	registryContainerPort = 5000
	kindNetwork           = "kind"
)

// One control-plane + three workers. containerdConfigPatches enables the
// per-registry config directory so the mirror hosts.toml the recipe drops on
// each node is honoured.
const kindConfig = `kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry]
      config_path = "/etc/containerd/certs.d"
nodes:
  - role: control-plane
  - role: worker
  - role: worker
  - role: worker
`

// KindClient is a semantic wrapper over kind (+ docker/flux/git) for local dev
// clusters. It implements the local core's LocalClusterAPI.
type KindClient struct{}

// NewKindClient builds a kind client.
func NewKindClient() *KindClient { return &KindClient{} }

// run executes a command (no shell), returning stdout; it raises a
// LocalClusterError on a missing executable or a non-zero exit.
func run(ctx context.Context, args []string, stdin string) (string, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", &LocalClusterError{msg: fmt.Sprintf("%q is not installed or not on PATH", args[0])}
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = strings.TrimSpace(stdout.String())
			}
			return "", &LocalClusterError{msg: fmt.Sprintf("`%s` failed:\n%s", strings.Join(args, " "), detail)}
		}
		return "", &LocalClusterError{msg: err.Error()}
	}
	return stdout.String(), nil
}

// query runs a read-only command, returning stdout or "" if it fails.
func query(ctx context.Context, args []string) string {
	out, err := run(ctx, args, "")
	if err != nil {
		return ""
	}
	return out
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// List returns the local cluster names.
func (c *KindClient) List(ctx context.Context) ([]string, error) {
	out, err := run(ctx, []string{"kind", "get", "clusters"}, "")
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// GetKubeconfig returns the named cluster's kubeconfig.
func (c *KindClient) GetKubeconfig(ctx context.Context, name string) (string, error) {
	return run(ctx, []string{"kind", "get", "kubeconfig", "--name", name}, "")
}

// GitProvenance returns (source, revision) describing the working tree for the
// OCI stamp.
func (c *KindClient) GitProvenance(ctx context.Context) (string, string, error) {
	source := strings.TrimSpace(query(ctx, []string{"git", "config", "--get", "remote.origin.url"}))
	if source == "" {
		toplevel := strings.TrimSpace(query(ctx, []string{"git", "rev-parse", "--show-toplevel"}))
		if toplevel == "" {
			toplevel = "."
		}
		source = "file://" + toplevel
	}
	branch := strings.TrimSpace(query(ctx, []string{"git", "rev-parse", "--abbrev-ref", "HEAD"}))
	if branch == "" {
		branch = "HEAD"
	}
	commit, err := run(ctx, []string{"git", "rev-parse", "HEAD"}, "")
	if err != nil {
		return "", "", err
	}
	revision := fmt.Sprintf("%s@sha1:%s", branch, strings.TrimSpace(commit))
	if strings.TrimSpace(query(ctx, []string{"git", "status", "--porcelain"})) != "" {
		revision += "-dirty"
	}
	return source, revision, nil
}

// PushArtifact pushes an OCI artifact to the registry via flux.
func (c *KindClient) PushArtifact(ctx context.Context, url, path, source, revision string) error {
	_, err := run(ctx, []string{
		"flux", "push", "artifact", url,
		"--path", path, "--source", source, "--revision", revision,
	}, "")
	return err
}

// ReconcileSource reconciles the Flux OCIRepository named name.
func (c *KindClient) ReconcileSource(ctx context.Context, name string) error {
	_, err := run(ctx, []string{"flux", "reconcile", "source", "oci", name}, "")
	return err
}

// Delete deletes the named cluster.
func (c *KindClient) Delete(ctx context.Context, name string) error {
	_, err := run(ctx, []string{"kind", "delete", "cluster", "--name", name}, "")
	return err
}

// Create provisions a kind cluster wired to an internal OCI registry (the
// canonical kind local-registry recipe).
func (c *KindClient) Create(ctx context.Context, name, registryName string, registryPort int) error {
	if err := c.ensureRegistry(ctx, registryName, registryPort); err != nil {
		return err
	}
	if _, err := run(ctx, []string{"kind", "create", "cluster", "--name", name, "--config", "-"}, kindConfig); err != nil {
		return err
	}
	if err := c.configureNodes(ctx, name, registryName, registryPort); err != nil {
		return err
	}
	return c.connectRegistryToNetwork(ctx, registryName)
}

func (c *KindClient) ensureRegistry(ctx context.Context, registryName string, registryPort int) error {
	state := query(ctx, []string{"docker", "inspect", "-f", "{{.State.Running}}", registryName})
	if strings.TrimSpace(state) == "true" {
		return nil
	}
	_, err := run(ctx, []string{
		"docker", "run", "-d", "--restart=always",
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", registryPort, registryContainerPort),
		"--network", "bridge", "--name", registryName, "registry:2",
	}, "")
	return err
}

func (c *KindClient) configureNodes(ctx context.Context, name, registryName string, registryPort int) error {
	registryDir := fmt.Sprintf("/etc/containerd/certs.d/localhost:%d", registryPort)
	hostsToml := fmt.Sprintf("[host.\"http://%s:%d\"]\n", registryName, registryContainerPort)
	nodes, err := run(ctx, []string{"kind", "get", "nodes", "--name", name}, "")
	if err != nil {
		return err
	}
	for _, node := range nonEmptyLines(nodes) {
		if _, err := run(ctx, []string{"docker", "exec", node, "mkdir", "-p", registryDir}, ""); err != nil {
			return err
		}
		if _, err := run(ctx, []string{"docker", "exec", "-i", node, "cp", "/dev/stdin", registryDir + "/hosts.toml"}, hostsToml); err != nil {
			return err
		}
	}
	return nil
}

func (c *KindClient) connectRegistryToNetwork(ctx context.Context, registryName string) error {
	connected := query(ctx, []string{
		"docker", "inspect", "-f",
		fmt.Sprintf("{{json .NetworkSettings.Networks.%s}}", kindNetwork), registryName,
	})
	if strings.TrimSpace(connected) != "" && strings.TrimSpace(connected) != "null" {
		return nil
	}
	_, err := run(ctx, []string{"docker", "network", "connect", kindNetwork, registryName}, "")
	return err
}
