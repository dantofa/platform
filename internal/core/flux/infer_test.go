package flux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeGit struct {
	remote, branch string
	err            error
}

func (f fakeGit) RemoteURL(context.Context) (string, error)     { return f.remote, f.err }
func (f fakeGit) CurrentBranch(context.Context) (string, error) { return f.branch, f.err }

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestInferGitSourceActionsPullRequestTracksTheHeadBranch(t *testing.T) {
	got, err := InferGitSource(context.Background(), env(map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com",
		"GITHUB_REPOSITORY": "dantofa/auth", "GITHUB_REF_NAME": "42/merge", "GITHUB_HEAD_REF": "feature-x",
	}), fakeGit{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://github.com/dantofa/auth" || got.Revision != "feature-x" {
		t.Errorf("got %+v, want the PR head branch, not the merge ref", got)
	}
}

func TestInferGitSourceActionsPushUsesRefName(t *testing.T) {
	got, err := InferGitSource(context.Background(), env(map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "dantofa/auth", "GITHUB_REF_NAME": "master",
	}), fakeGit{}, "", "")
	if err != nil || got.URL != "https://github.com/dantofa/auth" || got.Revision != "master" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestInferGitSourceRejectsTagRuns(t *testing.T) {
	_, err := InferGitSource(context.Background(), env(map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "o/r", "GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v1",
	}), fakeGit{}, "", "")
	if err == nil || !strings.Contains(err.Error(), "tag") {
		t.Fatalf("want a tag error, got %v", err)
	}
}

func TestInferGitSourceFallsBackToTheCheckout(t *testing.T) {
	got, err := InferGitSource(context.Background(), env(nil),
		fakeGit{remote: "git@github.com:dantofa/auth.git", branch: "dev"}, "", "")
	if err != nil || got.URL != "https://github.com/dantofa/auth" || got.Revision != "dev" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestInferGitSourceExplicitValuesWin(t *testing.T) {
	got, err := InferGitSource(context.Background(), env(map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "o/r", "GITHUB_REF_NAME": "master",
	}), fakeGit{}, "https://example.com/x/y", "pinned")
	if err != nil || got.URL != "https://example.com/x/y" || got.Revision != "pinned" ||
		got.URLFrom != "" || got.RevisionFrom != "" {
		t.Fatalf("explicit values must pass through uninferred: %+v err %v", got, err)
	}
}

func TestInferGitSourceNeedsSomethingToGoOn(t *testing.T) {
	if _, err := InferGitSource(context.Background(), env(nil), fakeGit{err: errors.New("no repo")}, "", ""); err == nil {
		t.Error("want an error with no Actions env and no checkout")
	}
	if _, err := InferGitSource(context.Background(), env(nil), fakeGit{remote: "https://github.com/o/r"}, "", ""); err == nil {
		t.Error("want an error on a detached HEAD")
	}
}

func TestNormalizeGitURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:dantofa/auth.git":               "https://github.com/dantofa/auth",
		"ssh://git@github.com:22/dantofa/auth.git":      "https://github.com/dantofa/auth",
		"https://github.com/dantofa/auth.git":           "https://github.com/dantofa/auth",
		"https://user:secret@github.com/dantofa/auth":   "https://github.com/dantofa/auth",
		"https://git.example.com:8443/dantofa/auth.git": "https://git.example.com:8443/dantofa/auth",
	} {
		got, err := normalizeGitURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeGitURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizeGitURL("/home/me/repo"); err == nil {
		t.Error("a filesystem path has no https equivalent; want an error")
	}
}
