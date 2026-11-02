// Package platform embeds the platform's GitOps tree (flux/) so a dctl binary
// carries exactly the manifests of the rev it was built from. Local bootstrap
// publishes this tree, so it needs no platform checkout and can run from any
// directory (a downstream repo included).
package platform

import "embed"

// Flux is the flux/ tree, rooted so paths keep their flux/ prefix.
//
//go:embed all:flux
var Flux embed.FS
