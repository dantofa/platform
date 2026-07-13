// Package version exposes the build-stamped version of the platform binaries.
package version

// Version is the platform version. It defaults to "dev" for plain `go build`
// and is overridden at release/package time via the linker, e.g.
//
//	-ldflags "-X github.com/dantofa/platform/internal/version.Version=1.2.3"
//
// The flake stamps it with a source-derived CalVer value (<YYYY.MM.DD>+g<rev>,
// with a -dirty suffix on an unclean tree).
var Version = "dev"
