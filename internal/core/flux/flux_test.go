package flux

import (
	"context"
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeEngine records the flux operations invoked against it, in order.
type fakeEngine struct {
	events        []string
	failOn        string // an event prefix that should return an error
	failErr       error
	lastConfigMap map[string]string // data of the last ApplyConfigMap call
	lastSecret    map[string][]byte // data of the last secret applied
	secrets       []string          // names of secrets already in flux-system
	lastRoot      ReconcileRoot     // the last ApplyReconcileRoot argument
}

func (f *fakeEngine) record(event string) error {
	f.events = append(f.events, event)
	if f.failOn != "" && event == f.failOn {
		return f.failErr
	}
	return nil
}

func (f *fakeEngine) Install(_ context.Context, version string) error {
	return f.record("install:" + version)
}

func (f *fakeEngine) CreateGitSource(_ context.Context, spec SourceSpec) error {
	return f.record("create-source:" + spec.Name + ":" + spec.URL + ":" + spec.Revision + secretSuffix(spec))
}

// secretSuffix renders a source's credential into the recorded event, so a test
// sees both that the source was registered and what it authenticates with. An
// anonymous source records nothing extra.
func secretSuffix(spec SourceSpec) string {
	if spec.SecretRef == "" {
		return ""
	}
	return ":secret=" + spec.SecretRef
}

func (f *fakeEngine) DeleteGitSource(_ context.Context, name string) error {
	return f.record("delete-source:" + name)
}

func (f *fakeEngine) DeleteKustomization(_ context.Context, name string) error {
	return f.record("delete-ks:" + name)
}

func (f *fakeEngine) CreateOCISource(_ context.Context, spec SourceSpec) error {
	return f.record("create-oci-source:" + spec.Name + ":" + spec.URL + ":" + spec.Revision + secretSuffix(spec))
}

func (f *fakeEngine) DeleteOCISource(_ context.Context, name string) error {
	return f.record("delete-oci-source:" + name)
}

func (f *fakeEngine) ApplyReconcileRoot(_ context.Context, root ReconcileRoot) error {
	f.lastRoot = root
	return f.record("apply-root:" + root.Name + ":" + root.SourceKind + "/" + root.SourceName + ":" + root.Path)
}

func (f *fakeEngine) ApplyConfigMap(_ context.Context, namespace, name string, data map[string]string) error {
	f.lastConfigMap = data
	return f.record("apply-cfgmap:" + namespace + "/" + name)
}

func (f *fakeEngine) SecretExists(_ context.Context, namespace, name string) (bool, error) {
	if err := f.record("secret-exists:" + namespace + "/" + name); err != nil {
		return false, err
	}
	return slices.Contains(f.secrets, name), nil
}

func (f *fakeEngine) ApplySecret(_ context.Context, namespace, name string, data map[string][]byte, _ map[string]string) error {
	f.lastSecret = data
	return f.record("apply-secret:" + namespace + "/" + name)
}

func (f *fakeEngine) ApplyDockerConfigSecret(_ context.Context, namespace, name string, config []byte) error {
	f.lastSecret = map[string][]byte{".dockerconfigjson": config}
	return f.record("apply-dockerconfig-secret:" + namespace + "/" + name)
}

func eq(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAddSource(t *testing.T) {
	e := &fakeEngine{}
	res, err := AddSource(context.Background(), e, e, SourceSpec{Type: SourceGit, Name: "app", URL: "https://git/app", Revision: "main"})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	eq(t, res.Source, "app")
	eq(t, res.Kind, "GitRepository")
	eq(t, res.URL, "https://git/app")
	eq(t, res.Revision, "main")
	eq(t, e.events[0], "create-source:app:https://git/app:main")
}

func TestAddSourceOCI(t *testing.T) {
	e := &fakeEngine{}
	res, err := AddSource(context.Background(), e, e, SourceSpec{Type: SourceOCI, Name: "app", URL: "oci://reg/app", Revision: "latest"})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	eq(t, res.Kind, "OCIRepository")
	eq(t, e.events[0], "create-oci-source:app:oci://reg/app:latest")
}

func TestAddSourceThreadsSecretRef(t *testing.T) {
	e := &fakeEngine{secrets: []string{"app-deploy-key"}}
	// A pre-existing credential (e.g. an SSH deploy key created out of band) is
	// referenced, never re-created: it is checked, then used.
	res, err := AddSource(context.Background(), e, e, SourceSpec{
		Type: SourceGit, Name: "app", URL: "ssh://git@github.com/org/app",
		Revision: "main", SecretRef: "app-deploy-key",
	})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	eq(t, res.SecretRef, "app-deploy-key")
	want := []string{
		"secret-exists:flux-system/app-deploy-key",
		"create-source:app:ssh://git@github.com/org/app:main:secret=app-deploy-key",
	}
	if len(e.events) != len(want) {
		t.Fatalf("events = %v, want %v", e.events, want)
	}
	for i := range want {
		eq(t, e.events[i], want[i])
	}
}

func TestAddSourceRejectsMissingSecretRef(t *testing.T) {
	e := &fakeEngine{} // no secrets in the cluster
	// The flux CLI writes the source and then blocks on readiness, so an
	// unresolvable secretRef costs its full timeout before it surfaces. Reject it
	// here instead, before anything is written.
	_, err := AddSource(context.Background(), e, e, SourceSpec{
		Type: SourceGit, Name: "app", URL: "https://github.com/org/app",
		Revision: "main", SecretRef: "typoed-name",
	})
	if err == nil {
		t.Fatal("expected an error for a secret-ref that does not resolve")
	}
	for _, want := range []string{"typoed-name", "flux-system", "--token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got %v", want, err)
		}
	}
	if len(e.events) != 1 || e.events[0] != "secret-exists:flux-system/typoed-name" {
		t.Errorf("no source should have been registered, got %v", e.events)
	}
}

func TestAddSourceMintSkipsTheExistenceCheck(t *testing.T) {
	e := &fakeEngine{} // no secrets yet — the token creates one
	if _, err := AddSource(context.Background(), e, e, SourceSpec{
		Type: SourceGit, Name: "app", URL: "https://github.com/org/app",
		Revision: "main", SecretRef: "app-creds", Token: "tok",
	}); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	// Minting is what makes the secret exist, so checking first would reject
	// every first run.
	for _, ev := range e.events {
		if strings.HasPrefix(ev, "secret-exists:") {
			t.Errorf("mint path must not pre-check the secret, got %v", e.events)
		}
	}
}

func TestAddSourceReferenceWithoutClusterAccessFails(t *testing.T) {
	e := &fakeEngine{}
	if _, err := AddSource(context.Background(), e, nil, SourceSpec{
		Type: SourceGit, Name: "app", URL: "https://github.com/org/app", SecretRef: "creds",
	}); err == nil {
		t.Fatal("expected an error when a secret-ref is given with no store")
	}
	if len(e.events) != 0 {
		t.Errorf("no source should have been registered, got %v", e.events)
	}
}

func TestAddSourceMintsGitCredentialBeforeRegistering(t *testing.T) {
	e := &fakeEngine{}
	res, err := AddSource(context.Background(), e, e, SourceSpec{
		Type: SourceGit, Name: "app", URL: "https://github.com/org/app",
		Revision: "main", Token: "ghp_secret",
	})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	// The secret must exist before the source references it, or the first
	// reconcile fails against an unresolvable secretRef.
	want := []string{
		"apply-secret:flux-system/app-auth",
		"create-source:app:https://github.com/org/app:main:secret=app-auth",
	}
	if len(e.events) != len(want) {
		t.Fatalf("events = %v, want %v", e.events, want)
	}
	for i := range want {
		eq(t, e.events[i], want[i])
	}
	// Basic auth in the shape source-controller reads, with the conventional
	// placeholder username a forge ignores for a PAT.
	eq(t, string(e.lastSecret["username"]), "git")
	eq(t, string(e.lastSecret["password"]), "ghp_secret")
	eq(t, res.SecretRef, "app-auth")
}

func TestAddSourceMintUsesSecretRefNameAndUsername(t *testing.T) {
	e := &fakeEngine{}
	// --secret-ref names the secret to mint when a token is given, rather than
	// selecting an existing one.
	if _, err := AddSource(context.Background(), e, e, SourceSpec{
		Type: SourceGit, Name: "app", URL: "https://gitlab.com/org/app", Revision: "main",
		SecretRef: "forge-credential", Token: "tok", Username: "deploy-bot",
	}); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	eq(t, e.events[0], "apply-secret:flux-system/forge-credential")
	eq(t, e.events[1], "create-source:app:https://gitlab.com/org/app:main:secret=forge-credential")
	eq(t, string(e.lastSecret["username"]), "deploy-bot")
}

func TestAddSourceMintsOCIPullSecret(t *testing.T) {
	e := &fakeEngine{}
	if _, err := AddSource(context.Background(), e, e, SourceSpec{
		Type: SourceOCI, Name: "app", URL: "oci://ghcr.io/org/app",
		Revision: "latest", Token: "ghp_secret",
	}); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	// An OCIRepository's secretRef only resolves against a dockerconfigjson
	// secret, so the OCI path must not mint the git-shaped basic-auth one.
	eq(t, e.events[0], "apply-dockerconfig-secret:flux-system/app-auth")
	eq(t, e.events[1], "create-oci-source:app:oci://ghcr.io/org/app:latest:secret=app-auth")
	got := string(e.lastSecret[".dockerconfigjson"])
	// Keyed by the registry host alone — a config keyed by the full artifact path
	// is silently ignored on pull.
	want := `{"auths":{"ghcr.io":{"username":"git","password":"ghp_secret","auth":"Z2l0OmdocF9zZWNyZXQ="}}}`
	if got != want {
		t.Errorf("dockerconfigjson =\n%s\nwant:\n%s", got, want)
	}
}

func TestAddSourceRejectsTokenOnSSHURL(t *testing.T) {
	// Basic auth cannot authenticate an SSH remote: failing here beats a source
	// that reconciles for minutes against a credential git will never send.
	for _, url := range []string{"ssh://git@github.com/org/app", "git@github.com:org/app.git"} {
		e := &fakeEngine{}
		_, err := AddSource(context.Background(), e, e, SourceSpec{
			Type: SourceGit, Name: "app", URL: url, Revision: "main", Token: "tok",
		})
		if err == nil {
			t.Fatalf("%s: expected an error for --token with an SSH URL", url)
		}
		if !strings.Contains(err.Error(), "--secret-ref") {
			t.Errorf("%s: error should point at --secret-ref, got %v", url, err)
		}
		if len(e.events) != 0 {
			t.Errorf("%s: nothing should have been written, got %v", url, e.events)
		}
	}
}

func TestAddSourceMintWithoutClusterAccessFails(t *testing.T) {
	e := &fakeEngine{}
	if _, err := AddSource(context.Background(), e, nil, SourceSpec{
		Type: SourceGit, Name: "app", URL: "https://github.com/org/app", Token: "tok",
	}); err == nil {
		t.Fatal("expected an error when a token is given with no applier")
	}
	if len(e.events) != 0 {
		t.Errorf("no source should have been registered, got %v", e.events)
	}
}

func TestSourceSpecSecretName(t *testing.T) {
	cases := []struct {
		name string
		spec SourceSpec
		want string
	}{
		{"anonymous", SourceSpec{Name: "app"}, ""},
		{"referenced", SourceSpec{Name: "app", SecretRef: "creds"}, "creds"},
		{"minted", SourceSpec{Name: "app", Token: "tok"}, "app-auth"},
		{"minted into a named secret", SourceSpec{Name: "app", SecretRef: "creds", Token: "tok"}, "creds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { eq(t, tc.spec.SecretName(), tc.want) })
	}
}

func TestRegistryHost(t *testing.T) {
	cases := []struct{ url, want string }{
		{"oci://ghcr.io/dantofa/platform", "ghcr.io"},
		{"oci://kind-registry:5000/local", "kind-registry:5000"}, // port preserved
		{"oci://ghcr.io", "ghcr.io"},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			got, err := RegistryHost(tc.url)
			if err != nil {
				t.Fatalf("RegistryHost(%q): %v", tc.url, err)
			}
			eq(t, got, tc.want)
		})
	}
	if _, err := RegistryHost("oci://"); err == nil {
		t.Error("expected an error for a URL with no host")
	}
}

func TestBootstrapAuthenticatesPrivateSource(t *testing.T) {
	e := &fakeEngine{}
	// A cluster bootstrapped against a private platform fork: the credential is
	// planted before Flux is pointed at the source.
	if _, err := Bootstrap(context.Background(), e, e, "",
		SourceSpec{
			Type: SourceGit, Name: DefaultSourceName, URL: "https://github.com/org/private",
			Revision: "master", Token: "tok",
		},
		nil, []ReconcileRoot{{Name: ClusterRootName, Path: DefaultSourcePath}}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	want := []string{
		"install:",
		"apply-secret:flux-system/platform-auth",
		"create-source:platform:https://github.com/org/private:master:secret=platform-auth",
		"apply-cfgmap:flux-system/cluster-vars",
		"apply-root:cluster:GitRepository/platform:" + DefaultSourcePath,
	}
	if len(e.events) != len(want) {
		t.Fatalf("events = %v, want %v", e.events, want)
	}
	for i := range want {
		eq(t, e.events[i], want[i])
	}
}

func TestBootstrapRejectsBadSourceAuthBeforeInstalling(t *testing.T) {
	e := &fakeEngine{}
	// Nothing may reach the cluster — not even the controllers — when the source
	// credential cannot work.
	if _, err := Bootstrap(context.Background(), e, e, "",
		SourceSpec{Type: SourceGit, Name: "platform", URL: "ssh://git@github.com/org/x", Token: "tok"},
		nil, nil); err == nil {
		t.Fatal("expected a validation error")
	}
	if len(e.events) != 0 {
		t.Errorf("expected no cluster writes, got %v", e.events)
	}
}

func TestAddKustomization(t *testing.T) {
	e := &fakeEngine{}
	// A downstream payload: reconcile the caller's manifests from their own git
	// source, resolving cluster-vars and waiting on the platform ingress.
	res, err := AddKustomization(context.Background(), e, KustomizationSpec{
		Type: SourceGit, Name: "app", Source: "app", Path: "./deploy",
		Substitute: true, DependsOn: []string{"ingress"},
	})
	if err != nil {
		t.Fatalf("AddKustomization: %v", err)
	}
	eq(t, res.Kustomization, "app")
	eq(t, res.SourceKind, "GitRepository")
	eq(t, res.Source, "app")
	eq(t, res.Path, "./deploy")
	// It applies a reconcile-root CR (so substitute/dependsOn are expressible),
	// not a plain `flux create kustomization`.
	eq(t, e.events[0], "apply-root:app:GitRepository/app:./deploy")
	if !e.lastRoot.Substitute {
		t.Error("expected Substitute to be threaded into the reconcile root")
	}
	if len(e.lastRoot.DependsOn) != 1 || e.lastRoot.DependsOn[0] != "ingress" {
		t.Errorf("DependsOn = %v, want [ingress]", e.lastRoot.DependsOn)
	}
}

func TestRemoveSourceAndKustomization(t *testing.T) {
	e := &fakeEngine{}
	if err := RemoveSource(context.Background(), e, SourceGit, "app"); err != nil {
		t.Fatalf("RemoveSource: %v", err)
	}
	if err := RemoveKustomization(context.Background(), e, "app"); err != nil {
		t.Fatalf("RemoveKustomization: %v", err)
	}
	eq(t, e.events[0], "delete-source:app")
	eq(t, e.events[1], "delete-ks:app")
}

func TestBootstrapOCIOrdersInstallSourceVarsThenRoots(t *testing.T) {
	e := &fakeEngine{}
	// The local shape: an OCI source and two roots, cluster after requirements.
	roots := []ReconcileRoot{
		{Name: LocalRequirementsRootName, Path: DefaultLocalSourcePath},
		{Name: ClusterRootName, Path: DefaultSourcePath, DependsOn: []string{LocalRequirementsRootName}, Substitute: true},
	}
	vars := map[string]string{VarBaseDomain: "127.0.0.1.nip.io", VarClusterName: "local"}
	res, err := Bootstrap(context.Background(), e, e, "",
		SourceSpec{Type: SourceOCI, Name: DefaultSourceName, URL: "oci://kind-registry:5000/local", Revision: "latest"},
		vars, roots)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	// cluster-vars is written after the source, before the roots that read it.
	want := []string{
		"install:",
		"create-oci-source:platform:oci://kind-registry:5000/local:latest",
		"apply-cfgmap:flux-system/cluster-vars",
		"apply-root:local-requirements:OCIRepository/platform:" + DefaultLocalSourcePath,
		"apply-root:cluster:OCIRepository/platform:" + DefaultSourcePath,
	}
	if len(e.events) != len(want) {
		t.Fatalf("events = %v, want %v", e.events, want)
	}
	for i := range want {
		eq(t, e.events[i], want[i])
	}
	// cluster-vars merges the source coordinates with the caller's vars.
	eq(t, e.lastConfigMap[VarSourceKind], "OCIRepository")
	eq(t, e.lastConfigMap[VarSourceName], "platform")
	eq(t, e.lastConfigMap[VarBaseDomain], "127.0.0.1.nip.io")
	eq(t, e.lastConfigMap[VarClusterName], "local")
	eq(t, res.Source, "platform")
	eq(t, res.SourceKind, "OCIRepository")
	if len(res.Kustomizations) != 2 || res.Kustomizations[0] != LocalRequirementsRootName || res.Kustomizations[1] != ClusterRootName {
		t.Fatalf("kustomizations = %v", res.Kustomizations)
	}
}

func TestBootstrapGitRegistersGitSource(t *testing.T) {
	e := &fakeEngine{}
	// The DOKS/downstream shape: a git source and a single cluster root.
	res, err := Bootstrap(context.Background(), e, e, "v2.3.0",
		SourceSpec{Type: SourceGit, Name: DefaultSourceName, URL: DefaultSourceURL, Revision: DefaultSourceBranch},
		map[string]string{VarBaseDomain: "dev.example.com"},
		[]ReconcileRoot{{Name: ClusterRootName, Path: DefaultSourcePath, Substitute: true}})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	want := []string{
		"install:v2.3.0",
		"create-source:platform:" + DefaultSourceURL + ":master",
		"apply-cfgmap:flux-system/cluster-vars",
		"apply-root:cluster:GitRepository/platform:" + DefaultSourcePath,
	}
	if len(e.events) != len(want) {
		t.Fatalf("events = %v, want %v", e.events, want)
	}
	for i := range want {
		eq(t, e.events[i], want[i])
	}
	eq(t, e.lastConfigMap[VarSourceKind], "GitRepository")
	eq(t, e.lastConfigMap[VarBaseDomain], "dev.example.com")
	eq(t, res.SourceKind, "GitRepository")
	eq(t, res.Revision, "master")
}

type fakeKustomizationStatuser struct {
	statuses   []KustomizationStatus
	err        error
	errOn      map[int]error // errors returned on specific call numbers
	readyAfter int           // become all-ready on this call number (0 = never flip)
	calls      int
}

func (f *fakeKustomizationStatuser) KustomizationStatuses(context.Context, string) ([]KustomizationStatus, error) {
	f.calls++
	if err, ok := f.errOn[f.calls]; ok {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.readyAfter > 0 && f.calls >= f.readyAfter {
		out := make([]KustomizationStatus, len(f.statuses))
		for i, s := range f.statuses {
			s.Status, s.Ready = "Current", true
			out[i] = s
		}
		return out, nil
	}
	return f.statuses, nil
}

func TestListKustomizationsEmptyIsNonNil(t *testing.T) {
	got, err := ListKustomizations(context.Background(), &fakeKustomizationStatuser{}, "")
	if err != nil {
		t.Fatal(err)
	}
	// A nil slice marshals to JSON `null`; a list command must render `[]`.
	if got == nil {
		t.Fatal("expected a non-nil empty slice, got nil")
	}
}

func TestVerifyKustomizationsOKWhenAllReady(t *testing.T) {
	f := &fakeKustomizationStatuser{statuses: []KustomizationStatus{
		{Name: "platform", Status: "Current", Ready: true},
		{Name: "velero", Status: "Current", Ready: true},
	}}
	statuses, ok, err := VerifyKustomizations(context.Background(), f, "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected ok")
	}
	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}
}

func TestVerifyKustomizationsFailsWhenAnyNotReady(t *testing.T) {
	f := &fakeKustomizationStatuser{statuses: []KustomizationStatus{
		{Name: "platform", Status: "Current", Ready: true},
		{Name: "velero", Status: "Failed", Ready: false},
	}}
	statuses, ok, err := VerifyKustomizations(context.Background(), f, "")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected not ok")
	}
	// The full list is still returned so the caller can show every status.
	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}
}

func TestVerifyKustomizationsWaitConverges(t *testing.T) {
	f := &fakeKustomizationStatuser{
		statuses:   []KustomizationStatus{{Name: "platform", Status: "InProgress", Ready: false}},
		readyAfter: 2, // not ready on the first poll, ready on the second
	}
	_, ok, err := VerifyKustomizationsWait(context.Background(), f, nil, "", time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected ok after convergence")
	}
	if f.calls < 2 {
		t.Errorf("expected at least 2 polls, got %d", f.calls)
	}
}

func TestVerifyKustomizationsWaitTimesOut(t *testing.T) {
	f := &fakeKustomizationStatuser{
		statuses: []KustomizationStatus{{Name: "velero", Status: "Failed", Ready: false}},
	}
	statuses, ok, err := VerifyKustomizationsWait(context.Background(), f, nil, "", 20*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if ok || len(statuses) != 1 {
		t.Errorf("expected a timed-out report with the problem, got ok=%v statuses=%v", ok, statuses)
	}
}

// The failure that broke CI twice: a managed API server drops one connection
// mid-wait. The wait exists to outlast conditions that resolve themselves, so a
// single blip must not end it.
func TestVerifyKustomizationsWaitSurvivesTransientError(t *testing.T) {
	f := &fakeKustomizationStatuser{
		statuses:   []KustomizationStatus{{Name: "platform", Status: "InProgress", Ready: false}},
		errOn:      map[int]error{2: &url.Error{Op: "Get", URL: "https://x/apis", Err: io.EOF}},
		readyAfter: 3,
	}
	statuses, ok, err := VerifyKustomizationsWait(context.Background(), f, nil, "", time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("a dropped connection must not fail the wait: %v", err)
	}
	if !ok {
		t.Error("expected ok once the stacks converged after the blip")
	}
	if len(statuses) != 1 {
		t.Errorf("expected the converged report, got %v", statuses)
	}
}

func TestVerifyKustomizationsWaitReportsPersistentTransientError(t *testing.T) {
	// Never recovering is still a failure — the caller must be told why, not
	// handed a silent not-ready.
	f := &fakeKustomizationStatuser{err: &url.Error{Op: "Get", Err: io.EOF}}
	_, ok, err := VerifyKustomizationsWait(context.Background(), f, nil, "", 20*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected the last transient error to surface on timeout")
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want it to wrap io.EOF", err)
	}
	if ok {
		t.Error("expected not ok")
	}
}

func TestVerifyKustomizationsWaitFailsFastOnDefinitiveError(t *testing.T) {
	// An RBAC or kubeconfig problem is an answer, not a blip: retrying it would
	// spend the caller's whole timeout to report the same thing.
	sentinel := errors.New(`kustomizations is forbidden: User cannot list resource`)
	f := &fakeKustomizationStatuser{err: sentinel}
	if _, _, err := VerifyKustomizationsWait(context.Background(), f, nil, "", time.Minute, time.Millisecond); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if f.calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry)", f.calls)
	}
}

func TestVerifyKustomizationsWaitKeepsLastGoodReport(t *testing.T) {
	// A blip on the final poll must not erase the statuses the caller would
	// otherwise print to explain what was still pending.
	f := &fakeKustomizationStatuser{
		statuses: []KustomizationStatus{{Name: "velero", Status: "Failed", Ready: false}},
		errOn:    map[int]error{2: io.EOF, 3: io.EOF, 4: io.EOF, 5: io.EOF},
	}
	statuses, _, _ := VerifyKustomizationsWait(context.Background(), f, nil, "", 15*time.Millisecond, time.Millisecond)
	if len(statuses) != 1 || statuses[0].Name != "velero" {
		t.Errorf("expected the last good report to survive, got %v", statuses)
	}
}

// fakeHelmReleaseStatuser returns a fixed set of statuses on every call,
// optionally becoming terminal only from a given call number on (0 = never),
// to simulate a release that starts healthy/progressing and later fails.
type fakeHelmReleaseStatuser struct {
	statuses   []HelmReleaseStatus
	err        error
	terminalAt int
	calls      int
}

func (f *fakeHelmReleaseStatuser) HelmReleaseStatuses(context.Context, string) ([]HelmReleaseStatus, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.terminalAt > 0 && f.calls >= f.terminalAt {
		out := make([]HelmReleaseStatus, len(f.statuses))
		for i, s := range f.statuses {
			s.Terminal = true
			out[i] = s
		}
		return out, nil
	}
	return f.statuses, nil
}

func TestVerifyKustomizationsWaitFailsFastOnTerminalHelmRelease(t *testing.T) {
	// The Kustomization itself would wait out the whole timeout (never reports
	// ready), but the HelmRelease underneath it is already terminal -- the
	// wait must return immediately with the cause attached, not after 1 minute.
	ks := &fakeKustomizationStatuser{
		statuses: []KustomizationStatus{{Name: "cert-manager", Status: "InProgress", Ready: false}},
	}
	hs := &fakeHelmReleaseStatuser{statuses: []HelmReleaseStatus{{
		Namespace: "cert-manager", Name: "cert-manager",
		Reason: "InstallFailed", Message: "failed calling webhook \"validate.kyverno.svc-fail\"",
		Terminal: true,
	}}}
	start := time.Now()
	_, ok, err := VerifyKustomizationsWait(context.Background(), ks, hs, "", time.Minute, time.Millisecond)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("expected an immediate fail-fast, took %v", elapsed)
	}
	if ok {
		t.Error("expected not ok")
	}
	var termErr *TerminalHelmReleaseError
	if !errors.As(err, &termErr) {
		t.Fatalf("expected a *TerminalHelmReleaseError, got %v", err)
	}
	if termErr.Name != "cert-manager" || termErr.Reason != "InstallFailed" {
		t.Errorf("unexpected error contents: %+v", termErr)
	}
}

func TestVerifyKustomizationsWaitIgnoresNonTerminalHelmRelease(t *testing.T) {
	// A release still progressing must not trip the fail-fast; the wait
	// proceeds to normal convergence.
	ks := &fakeKustomizationStatuser{
		statuses:   []KustomizationStatus{{Name: "cert-manager", Status: "InProgress", Ready: false}},
		readyAfter: 2,
	}
	hs := &fakeHelmReleaseStatuser{statuses: []HelmReleaseStatus{{
		Namespace: "cert-manager", Name: "cert-manager", Reason: "Progressing", Terminal: false,
	}}}
	_, ok, err := VerifyKustomizationsWait(context.Background(), ks, hs, "", time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected ok once the Kustomization converged")
	}
}

func TestVerifyKustomizationsWaitSurvivesHelmReleaseReadError(t *testing.T) {
	// A transient failure reading HelmReleases must not abort the wait --
	// only the Kustomization-level polling owns retry/timeout semantics.
	ks := &fakeKustomizationStatuser{
		statuses:   []KustomizationStatus{{Name: "cert-manager", Status: "InProgress", Ready: false}},
		readyAfter: 2,
	}
	hs := &fakeHelmReleaseStatuser{err: io.EOF}
	_, ok, err := VerifyKustomizationsWait(context.Background(), ks, hs, "", time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("a HelmRelease read error must not fail the wait: %v", err)
	}
	if !ok {
		t.Error("expected ok once the Kustomization converged")
	}
}

func TestVerifyKustomizationsWaitNilHelmReleaseStatuserSkipsCheck(t *testing.T) {
	ks := &fakeKustomizationStatuser{
		statuses:   []KustomizationStatus{{Name: "cert-manager", Status: "InProgress", Ready: false}},
		readyAfter: 2,
	}
	_, ok, err := VerifyKustomizationsWait(context.Background(), ks, nil, "", time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected ok once the Kustomization converged")
	}
}

func TestTerminalHelmReleaseErrorMessage(t *testing.T) {
	err := &TerminalHelmReleaseError{
		Namespace: "cert-manager", Name: "cert-manager",
		Reason: "InstallFailed", Message: "boom",
		Events: []string{"FailedWebhook: connection refused"},
	}
	msg := err.Error()
	for _, want := range []string{"cert-manager/cert-manager", "InstallFailed", "boom", "FailedWebhook: connection refused"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}
}

func TestBootstrapStopsOnInstallFailure(t *testing.T) {
	sentinel := errors.New("install boom")
	e := &fakeEngine{failOn: "install:", failErr: sentinel}
	if _, err := Bootstrap(context.Background(), e, e, "",
		SourceSpec{Type: SourceOCI, Name: "x"}, nil,
		[]ReconcileRoot{{Name: ClusterRootName, Path: "./flux"}}); !errors.Is(err, sentinel) {
		t.Fatalf("expected install error, got %v", err)
	}
	// No source/root attempted after install failed.
	if len(e.events) != 1 {
		t.Fatalf("expected only the install attempt, got %v", e.events)
	}
}

func TestDNSZone(t *testing.T) {
	cases := []struct{ base, want string }{
		{"preview.dantofa.dev", "dantofa.dev"},
		{"dantofa.com", "dantofa.com"},
		{"local.dantofa.dev", "dantofa.dev"},
		{"a.b.dantofa.com", "dantofa.com"},
		{"dantofa.dev.", "dantofa.dev"}, // trailing dot tolerated
	}
	for _, tc := range cases {
		t.Run(tc.base, func(t *testing.T) {
			got, err := DNSZone(tc.base)
			if err != nil {
				t.Fatalf("DNSZone(%q): %v", tc.base, err)
			}
			eq(t, got, tc.want)
		})
	}
}

func TestValidateTLSIssuer(t *testing.T) {
	cases := []struct {
		issuer  string
		wantErr bool
	}{
		{"selfsigned", false},
		{"letsencrypt", false},
		{"staging", false},
		{"", true},
		{"self-signed", true},
		{"letsencrypt-staging", true},
	}
	for _, tc := range cases {
		t.Run(tc.issuer, func(t *testing.T) {
			if err := ValidateTLSIssuer(tc.issuer); tc.wantErr != (err != nil) {
				t.Fatalf("ValidateTLSIssuer(%q) err=%v, wantErr=%v", tc.issuer, err, tc.wantErr)
			}
		})
	}
}

func TestACMEReconcileRoots(t *testing.T) {
	t.Run("selfsigned has no ACME layer", func(t *testing.T) {
		roots, ok := ACMEReconcileRoots(TLSIssuerSelfSigned)
		if ok || roots != nil {
			t.Fatalf("ACMEReconcileRoots(selfsigned) = (%v, %v), want (nil, false)", roots, ok)
		}
	})

	// Both ACME issuers share the same two ordered roots, distinguished only by
	// the substituted ${tls_issuer}/${acme_server} on the ClusterIssuer.
	for _, issuer := range []string{TLSIssuerLetsEncrypt, TLSIssuerStaging} {
		t.Run(issuer, func(t *testing.T) {
			roots, ok := ACMEReconcileRoots(issuer)
			if !ok {
				t.Fatalf("ACMEReconcileRoots(%q) ok=false, want true", issuer)
			}
			if len(roots) != 2 {
				t.Fatalf("ACMEReconcileRoots(%q) returned %d roots, want 2", issuer, len(roots))
			}

			// 1. The DNS-01 token ExternalSecret (cloudflare-api-token): no ${...}
			// placeholders (no substitution), gated on the bitwarden store and the
			// cert-manager namespace it targets.
			es := roots[0]
			if es.Name != CloudflareAPITokenRootName || es.Path != DefaultCloudflareAPITokenPath {
				t.Fatalf("root[0] = {Name:%q Path:%q}, want {Name:%q Path:%q}",
					es.Name, es.Path, CloudflareAPITokenRootName, DefaultCloudflareAPITokenPath)
			}
			if es.Substitute {
				t.Fatalf("root[0] Substitute=true, want false (ExternalSecret has no ${...})")
			}
			assertDeps(t, "root[0]", es.DependsOn, CertManagerConfigName, ESOConfigName)

			// 2. The letsencrypt ClusterIssuer: substituted for its identity, and
			// ordered after the token ExternalSecret so the Secret has synced first.
			ci := roots[1]
			if ci.Name != LetsEncryptRootName || ci.Path != DefaultLetsEncryptPath {
				t.Fatalf("root[1] = {Name:%q Path:%q}, want {Name:%q Path:%q}",
					ci.Name, ci.Path, LetsEncryptRootName, DefaultLetsEncryptPath)
			}
			if !ci.Substitute {
				t.Fatalf("root[1] Substitute=false, want true (${tls_issuer}/${acme_server})")
			}
			assertDeps(t, "root[1]", ci.DependsOn, CertManagerConfigName, CloudflareAPITokenRootName)
		})
	}
}

func TestDOKSIngressRoots(t *testing.T) {
	// Default (no --dolb): a single Cloudflare Tunnel root, the same as kind — no
	// LoadBalancer, no external-dns, no ACME. tlsIssuer is inert here, so even an
	// ACME value yields only the tunnel root.
	for _, issuer := range []string{TLSIssuerSelfSigned, TLSIssuerLetsEncrypt, TLSIssuerStaging} {
		t.Run("tunnel/"+issuer, func(t *testing.T) {
			roots := DOKSIngressRoots(false, issuer)
			if len(roots) != 1 {
				t.Fatalf("DOKSIngressRoots(false, %q) returned %d roots, want 1: %+v", issuer, len(roots), roots)
			}
			r := roots[0]
			if r.Name != IngressRootName || r.Path != DefaultTunnelIngressPath {
				t.Fatalf("tunnel root = {Name:%q Path:%q}, want {Name:%q Path:%q}",
					r.Name, r.Path, IngressRootName, DefaultTunnelIngressPath)
			}
			if !r.Substitute {
				t.Fatalf("tunnel root Substitute=false, want true (tunnel_name is ${cluster_name})")
			}
			assertDeps(t, "tunnel root", r.DependsOn, ESOConfigName)
		})
	}

	// --dolb, selfsigned: external-dns + Traefik, no ACME layer. The ingress
	// (Traefik) root waits only on cert-manager-config (the selfsigned issuer).
	t.Run("dolb/selfsigned", func(t *testing.T) {
		roots := DOKSIngressRoots(true, TLSIssuerSelfSigned)
		if len(roots) != 2 {
			t.Fatalf("DOKSIngressRoots(true, selfsigned) returned %d roots, want 2: %+v", len(roots), roots)
		}
		ingress := findRoot(t, roots, IngressRootName)
		if ingress.Path != DefaultRemoteIngressPath {
			t.Fatalf("ingress root Path=%q, want %q", ingress.Path, DefaultRemoteIngressPath)
		}
		assertDeps(t, "ingress root", ingress.DependsOn, CertManagerConfigName)
		edns := findRoot(t, roots, ExternalDNSRootName)
		if edns.Path != DefaultExternalDNSPath {
			t.Fatalf("external-dns root Path=%q, want %q", edns.Path, DefaultExternalDNSPath)
		}
		assertDeps(t, "external-dns root", edns.DependsOn, ESOConfigName)
	})

	// --dolb, ACME issuer: external-dns + the two ACME roots + Traefik, and the
	// ingress root additionally waits on the letsencrypt issuer.
	for _, issuer := range []string{TLSIssuerLetsEncrypt, TLSIssuerStaging} {
		t.Run("dolb/"+issuer, func(t *testing.T) {
			roots := DOKSIngressRoots(true, issuer)
			if len(roots) != 4 {
				t.Fatalf("DOKSIngressRoots(true, %q) returned %d roots, want 4: %+v", issuer, len(roots), roots)
			}
			findRoot(t, roots, ExternalDNSRootName)
			findRoot(t, roots, CloudflareAPITokenRootName)
			findRoot(t, roots, LetsEncryptRootName)
			ingress := findRoot(t, roots, IngressRootName)
			assertDeps(t, "ingress root", ingress.DependsOn, CertManagerConfigName, LetsEncryptRootName)
		})
	}
}

// findRoot returns the reconcile root named name, failing if it is absent.
func findRoot(t *testing.T, roots []ReconcileRoot, name string) ReconcileRoot {
	t.Helper()
	for _, r := range roots {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no reconcile root named %q in %+v", name, roots)
	return ReconcileRoot{}
}

// assertDeps checks that got is exactly the set want, order-independent.
func assertDeps(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	if len(got) != len(wantSet) {
		t.Fatalf("%s DependsOn=%v, want %v", label, got, want)
	}
	for _, d := range got {
		if !wantSet[d] {
			t.Fatalf("%s unexpected dependency %q (want %v)", label, d, want)
		}
	}
}

func TestACMEServerURL(t *testing.T) {
	cases := []struct {
		issuer string
		want   string
	}{
		{TLSIssuerLetsEncrypt, ACMEServerLetsEncrypt},
		{TLSIssuerStaging, ACMEServerStaging},
		{TLSIssuerSelfSigned, ""},
	}
	for _, tc := range cases {
		t.Run(tc.issuer, func(t *testing.T) {
			if got := ACMEServerURL(tc.issuer); got != tc.want {
				t.Fatalf("ACMEServerURL(%q) = %q, want %q", tc.issuer, got, tc.want)
			}
		})
	}
}

func TestValidateBitwardenConfig(t *testing.T) {
	cases := []struct {
		name                    string
		token, projectID, orgID string
		wantErr                 bool
	}{
		{"fully configured", "tok", "proj", "org", false},
		{"not configured at all", "", "", "", false},
		{"token only", "tok", "", "", false},
		{"project without token", "", "proj", "", true},
		{"org without token", "", "", "org", true},
		{"project and org without token", "", "proj", "org", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBitwardenConfig(tc.token, tc.projectID, tc.orgID)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidateBitwardenConfig(%q,%q,%q) err=%v, wantErr=%v",
					tc.token, tc.projectID, tc.orgID, err, tc.wantErr)
			}
		})
	}
}
