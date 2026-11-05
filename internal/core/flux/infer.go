package flux

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// GitInfo is the local-checkout surface InferGitSource falls back to when it is
// not running under GitHub Actions. Satisfied by the git client adapter.
type GitInfo interface {
	RemoteURL(ctx context.Context) (string, error)
	CurrentBranch(ctx context.Context) (string, error)
}

// InferredGit is a git source's resolved URL and revision, each with a short
// description of where it came from (so the caller can say what it inferred).
type InferredGit struct {
	URL          string
	Revision     string
	URLFrom      string
	RevisionFrom string
}

// InferGitSource resolves the URL and revision of a git source for the repository
// the process runs in, so a CI job does not have to assemble them. An explicit
// url or revision wins and is returned as-is.
//
// Under GitHub Actions (GITHUB_ACTIONS=true) the URL is GITHUB_SERVER_URL +
// GITHUB_REPOSITORY and the revision is the PR head branch (GITHUB_HEAD_REF, set
// only on pull_request events) or else GITHUB_REF_NAME. The head branch matters:
// on a pull_request event GITHUB_REF_NAME is "<n>/merge", a ref a source must not
// track. Outside Actions it falls back to the checkout's `origin` remote and
// current branch. Remotes are normalized to https (credentials stripped) so the
// result works with --token. A fork PR's head branch lives in the fork, not in
// GITHUB_REPOSITORY, so pass --url explicitly there.
func InferGitSource(ctx context.Context, getenv func(string) string, git GitInfo, url, revision string) (InferredGit, error) {
	got := InferredGit{URL: url, Revision: revision}
	ci := getenv("GITHUB_ACTIONS") == "true"

	if got.URL == "" {
		if ci && getenv("GITHUB_REPOSITORY") != "" {
			server := getenv("GITHUB_SERVER_URL")
			if server == "" {
				server = "https://github.com"
			}
			got.URL = strings.TrimSuffix(server, "/") + "/" + getenv("GITHUB_REPOSITORY")
			got.URLFrom = "GITHUB_SERVER_URL/GITHUB_REPOSITORY"
		} else {
			raw, err := git.RemoteURL(ctx)
			if err != nil || raw == "" {
				return InferredGit{}, errors.New("cannot infer the repository URL: not running under GitHub Actions " +
					"and the checkout has no `origin` remote; pass --url")
			}
			norm, err := normalizeGitURL(raw)
			if err != nil {
				return InferredGit{}, err
			}
			got.URL, got.URLFrom = norm, "git remote origin"
		}
	}

	if got.Revision == "" {
		switch {
		case ci && getenv("GITHUB_HEAD_REF") != "":
			got.Revision, got.RevisionFrom = getenv("GITHUB_HEAD_REF"), "GITHUB_HEAD_REF"
		case ci && getenv("GITHUB_REF_TYPE") == "tag":
			return InferredGit{}, fmt.Errorf("the workflow ran on tag %q, but a git source tracks a branch; pass --revision",
				getenv("GITHUB_REF_NAME"))
		case ci && getenv("GITHUB_REF_NAME") != "":
			got.Revision, got.RevisionFrom = getenv("GITHUB_REF_NAME"), "GITHUB_REF_NAME"
		default:
			branch, err := git.CurrentBranch(ctx)
			if err != nil || branch == "" {
				return InferredGit{}, errors.New("cannot infer the revision: not running under GitHub Actions " +
					"and the checkout is on a detached HEAD; pass --revision")
			}
			got.Revision, got.RevisionFrom = branch, "current git branch"
		}
	}
	return got, nil
}

// normalizeGitURL turns a git remote into the https URL a GitRepository source
// can use with --token: scp-like ("git@host:org/repo.git") and ssh:// remotes
// become https, a trailing ".git" is dropped, and any embedded credentials are
// stripped so they are never copied into a cluster object or the printed note.
func normalizeGitURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	// scp-like syntax has no scheme: user@host:path.
	if !strings.Contains(raw, "://") {
		at := strings.Index(raw, "@")
		colon := strings.Index(raw, ":")
		if at >= 0 && colon > at {
			raw = "ssh://" + raw[:colon] + "/" + raw[colon+1:]
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("cannot derive an https repository URL from remote %q; pass --url", raw)
	}
	switch u.Scheme {
	case "https", "http", "ssh", "git":
	default:
		return "", fmt.Errorf("cannot derive an https repository URL from remote %q; pass --url", raw)
	}
	host := u.Hostname() // drops the SSH port (22) and any userinfo
	if u.Scheme == "https" || u.Scheme == "http" {
		host = u.Host // an https port is meaningful
	}
	return "https://" + host + "/" + strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git"), nil
}
