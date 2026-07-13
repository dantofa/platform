// Package flux is an adapter over the flux CLI (bundled in the package closure)
// for installing Flux into a cluster and composing GitOps sources and
// kustomizations. It shells out with an explicit --kubeconfig when one is set,
// so it targets a specific cluster (otherwise it uses the flux CLI's own
// default resolution: $KUBECONFIG / ~/.kube/config).
package flux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	fluxcore "github.com/dantofa/platform/internal/core/flux"
)

// fluxNamespace is where Flux installs its controllers and where sources /
// kustomizations are created.
const fluxNamespace = "flux-system"

// Client satisfies the core flux Engine, running the flux CLI against a
// specific cluster's kubeconfig.
var _ fluxcore.Engine = (*Client)(nil)

// Client runs the flux CLI against a specific cluster's kubeconfig.
type Client struct {
	kubeconfig string
}

// New builds a flux client bound to a kubeconfig path. An empty path defers to
// the flux CLI's own kubeconfig resolution.
func New(kubeconfigPath string) *Client { return &Client{kubeconfig: kubeconfigPath} }

func (c *Client) run(ctx context.Context, args ...string) error {
	full := args
	if c.kubeconfig != "" {
		full = append([]string{"--kubeconfig", c.kubeconfig}, args...)
	}
	cmd := exec.CommandContext(ctx, "flux", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("`flux` is not installed or not on PATH")
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("`flux %s` failed: %s", strings.Join(args, " "), detail)
	}
	return nil
}

// Install installs the Flux controllers. An empty version uses the flux CLI's
// own version; otherwise the given version's components are installed.
func (c *Client) Install(ctx context.Context, version string) error {
	args := []string{"install"}
	if version != "" {
		args = append(args, "--version", version)
	}
	return c.run(ctx, args...)
}

// CreateGitSource registers (create-or-update) a GitRepository source.
func (c *Client) CreateGitSource(ctx context.Context, name, url, branch string) error {
	return c.run(ctx, "create", "source", "git", name,
		"--url", url, "--branch", branch, "--interval", "1m",
		"--namespace", fluxNamespace)
}

// DeleteGitSource removes a GitRepository source.
func (c *Client) DeleteGitSource(ctx context.Context, name string) error {
	return c.run(ctx, "delete", "source", "git", name,
		"--silent", "--namespace", fluxNamespace)
}

// CreateKustomization registers (create-or-update) a Kustomization reconciling
// the given path from the named source. sourceKind is the source CRD kind
// (GitRepository or OCIRepository) the sourceRef points at.
func (c *Client) CreateKustomization(ctx context.Context, name, sourceKind, source, path string) error {
	return c.run(ctx, "create", "kustomization", name,
		"--source", sourceKind+"/"+source, "--path", path,
		"--prune=true", "--interval", "10m", "--namespace", fluxNamespace)
}

// CreateOCISource registers (create-or-update) an OCIRepository source at the
// given tag. insecure allows a plain-HTTP registry (the in-cluster kind
// registry); leave it off for TLS registries such as ghcr.io.
func (c *Client) CreateOCISource(ctx context.Context, name, url, tag string, insecure bool) error {
	args := []string{
		"create", "source", "oci", name,
		"--url", url, "--tag", tag, "--interval", "1m", "--namespace", fluxNamespace,
	}
	if insecure {
		args = append(args, "--insecure")
	}
	return c.run(ctx, args...)
}

// DeleteOCISource removes an OCIRepository source.
func (c *Client) DeleteOCISource(ctx context.Context, name string) error {
	return c.run(ctx, "delete", "source", "oci", name,
		"--silent", "--namespace", fluxNamespace)
}

// DeleteKustomization removes a Kustomization.
func (c *Client) DeleteKustomization(ctx context.Context, name string) error {
	return c.run(ctx, "delete", "kustomization", name,
		"--silent", "--namespace", fluxNamespace)
}
