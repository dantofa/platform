// Package git is the local-checkout adapter: it answers questions about the git
// repository in the current directory by shelling out to the git CLI.
package git

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Local reads the repository in the current working directory.
type Local struct{}

// New returns a Local adapter.
func New() Local { return Local{} }

// RemoteURL returns the URL of the `origin` remote.
func (Local) RemoteURL(ctx context.Context) (string, error) {
	return output(ctx, "config", "--get", "remote.origin.url")
}

// CurrentBranch returns the checked-out branch name, or "" on a detached HEAD.
func (Local) CurrentBranch(ctx context.Context) (string, error) {
	return output(ctx, "branch", "--show-current")
}

func output(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
