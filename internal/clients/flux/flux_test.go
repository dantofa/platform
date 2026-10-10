package flux

import (
	"strings"
	"testing"

	fluxcore "github.com/dantofa/platform/internal/core/flux"
)

// joined renders an arg slice for comparison, so a diff points at the flag that
// moved rather than at an index.
func joined(args []string) string { return strings.Join(args, " ") }

func TestGitSourceArgs(t *testing.T) {
	cases := []struct {
		name string
		spec fluxcore.SourceSpec
		want string
	}{
		{
			name: "anonymous",
			spec: fluxcore.SourceSpec{Name: "platform", URL: "https://github.com/dantofa/platform", Revision: "master"},
			want: "create source git platform --url https://github.com/dantofa/platform " +
				"--branch master --interval 1m --silent --namespace flux-system",
		},
		{
			name: "authenticated",
			spec: fluxcore.SourceSpec{
				Name: "app", URL: "https://github.com/org/private", Revision: "main",
				SecretRef: "app-auth",
			},
			want: "create source git app --url https://github.com/org/private " +
				"--branch main --interval 1m --silent --namespace flux-system --secret-ref app-auth",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joined(gitSourceArgs(tc.spec)); got != tc.want {
				t.Errorf("gitSourceArgs =\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestInstallArgsAlwaysIncludesImageAutomation(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    string
	}{
		{
			name: "no version",
			want: "install --components-extra=image-reflector-controller,image-automation-controller",
		},
		{
			name: "pinned version", version: "v2.3.0",
			want: "install --components-extra=image-reflector-controller,image-automation-controller --version v2.3.0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joined(installArgs(tc.version)); got != tc.want {
				t.Errorf("installArgs(%q) =\n%s\nwant:\n%s", tc.version, got, tc.want)
			}
		})
	}
}

// --insecure is an OCI-only flag; the git source has no such option and would be
// rejected by the flux CLI, so Insecure must not leak across the two builders.
func TestGitSourceArgsIgnoresInsecure(t *testing.T) {
	args := joined(gitSourceArgs(fluxcore.SourceSpec{
		Name: "app", URL: "https://git/app", Revision: "main", Insecure: true,
	}))
	if strings.Contains(args, "--insecure") {
		t.Errorf("git source args must not carry --insecure: %s", args)
	}
}

func TestOCISourceArgs(t *testing.T) {
	cases := []struct {
		name string
		spec fluxcore.SourceSpec
		want string
	}{
		{
			name: "anonymous",
			spec: fluxcore.SourceSpec{Name: "platform", URL: "oci://ghcr.io/dantofa/platform", Revision: "latest"},
			want: "create source oci platform --url oci://ghcr.io/dantofa/platform " +
				"--tag latest --interval 1m --namespace flux-system",
		},
		{
			name: "insecure local registry",
			spec: fluxcore.SourceSpec{
				Name: "platform", URL: "oci://kind-registry:5000/local", Revision: "latest", Insecure: true,
			},
			want: "create source oci platform --url oci://kind-registry:5000/local " +
				"--tag latest --interval 1m --namespace flux-system --insecure",
		},
		{
			name: "authenticated",
			spec: fluxcore.SourceSpec{
				Name: "app", URL: "oci://ghcr.io/org/private", Revision: "v1", SecretRef: "app-auth",
			},
			want: "create source oci app --url oci://ghcr.io/org/private " +
				"--tag v1 --interval 1m --namespace flux-system --secret-ref app-auth",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joined(ociSourceArgs(tc.spec)); got != tc.want {
				t.Errorf("ociSourceArgs =\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}
