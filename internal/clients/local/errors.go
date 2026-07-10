// Package local is the adapter over the kind / docker / flux / git CLIs for
// local development clusters. It shells out to external tools; failures are
// translated into LocalClusterError so callers above stay free of os/exec
// details.
package local

// LocalClusterError is a failed local-tool invocation or git read.
type LocalClusterError struct{ msg string }

func (e *LocalClusterError) Error() string { return e.msg }
