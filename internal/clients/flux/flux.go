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
	return c.run(ctx, installArgs(version)...)
}

// installArgs builds the `flux install` invocation. Always installs the
// image-reflector and image-automation controllers alongside Flux's default
// set (source/kustomize/helm/notification) -- not a caller choice, so every
// cluster type gets continuous image delivery uniformly rather than some
// clusters having it and others not. Each costs the same requests as any
// other controller (100m CPU / 64Mi memory, per Flux's own install
// manifests), so this is not a disproportionate addition. A downstream still
// opts in per-app by declaring its own ImageRepository/ImagePolicy/
// ImageUpdateAutomation; installing the controllers only makes that possible.
func installArgs(version string) []string {
	args := []string{"install", "--components-extra=image-reflector-controller,image-automation-controller"}
	if version != "" {
		args = append(args, "--version", version)
	}
	return args
}

// CreateGitSource registers (create-or-update) a GitRepository source.
func (c *Client) CreateGitSource(ctx context.Context, spec fluxcore.SourceSpec) error {
	return c.run(ctx, gitSourceArgs(spec)...)
}

// gitSourceArgs builds the `flux create source git` invocation for a spec. Split
// out so the flag composition is asserted directly in tests — a misplaced or
// dropped flag here is otherwise only visible as a mis-shaped object in a live
// cluster.
//
// --silent is unconditional: on an SSH URL with no --secret-ref the flux CLI
// generates a deploy key and blocks on an interactive confirmation, which in CI
// is a hang rather than a failure.
func gitSourceArgs(spec fluxcore.SourceSpec) []string {
	args := []string{
		"create", "source", "git", spec.Name,
		"--url", spec.URL, "--branch", spec.Revision, "--interval", "1m",
		"--silent", "--namespace", fluxNamespace,
	}
	if spec.SecretRef != "" {
		args = append(args, "--secret-ref", spec.SecretRef)
	}
	return args
}

// DeleteGitSource removes a GitRepository source.
func (c *Client) DeleteGitSource(ctx context.Context, name string) error {
	return c.run(ctx, "delete", "source", "git", name,
		"--silent", "--namespace", fluxNamespace)
}

// CreateOCISource registers (create-or-update) an OCIRepository source at the
// spec's tag. spec.Insecure allows a plain-HTTP registry (the in-cluster kind
// registry); leave it off for TLS registries such as ghcr.io.
func (c *Client) CreateOCISource(ctx context.Context, spec fluxcore.SourceSpec) error {
	return c.run(ctx, ociSourceArgs(spec)...)
}

// ociSourceArgs builds the `flux create source oci` invocation for a spec. The
// --secret-ref it passes must name a kubernetes.io/dockerconfigjson secret (an
// image pull secret), unlike the git source's basic-auth/SSH secret.
func ociSourceArgs(spec fluxcore.SourceSpec) []string {
	args := []string{
		"create", "source", "oci", spec.Name,
		"--url", spec.URL, "--tag", spec.Revision, "--interval", "1m",
		"--namespace", fluxNamespace,
	}
	if spec.Insecure {
		args = append(args, "--insecure")
	}
	if spec.SecretRef != "" {
		args = append(args, "--secret-ref", spec.SecretRef)
	}
	return args
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
