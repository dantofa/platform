package digitalocean

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	cfclient "github.com/dantofa/platform/internal/clients/cloudflare"
	"github.com/dantofa/platform/internal/core/edgecert"
	fluxcore "github.com/dantofa/platform/internal/core/flux"
	"github.com/dantofa/platform/internal/render"
)

// certClient builds the Cloudflare client for edge certificates from token, or
// from $CLOUDFLARE_CERT_TOKEN when token is empty. Certificates use their own
// credential, never the in-cluster one, so a missing token is an error naming it.
func certClient(token string) (edgecert.API, error) {
	if token == "" {
		token = os.Getenv(cloudflareEnvVar)
	}
	if token == "" {
		return nil, fmt.Errorf("no Cloudflare certificate token: set $%s or pass --cloudflare-token", cloudflareEnvVar)
	}
	return cfclient.New(token)
}

// newEdgeCertCmd exposes the cluster edge certificate as three primitives: the
// readiness gate `cluster verify health` calls, and the request/delete halves of
// the lifecycle the CI drill drives without needing a cluster.
func newEdgeCertCmd() *cobra.Command {
	var baseDomain, zone, token string
	ec := &cobra.Command{
		Use:   "edge-cert",
		Short: "Manage a base domain's Cloudflare edge certificate.",
		Long: "Order, verify and delete the Advanced Certificate for *.<base-domain> and " +
			"<base-domain>. Bootstrap and `cluster delete` do this for you; these are for " +
			"the readiness check and for exercising the lifecycle directly. Uses " +
			"$" + cloudflareEnvVar + " (SSL and Certificates: Edit, Zone: Read).",
		SilenceUsage: true,
	}
	pf := ec.PersistentFlags()
	pf.StringVar(&baseDomain, "base-domain", "", "Cluster ingress FQDN the certificate covers. Required.")
	_ = ec.MarkPersistentFlagRequired("base-domain")
	pf.StringVar(&zone, "zone", "", "Cloudflare zone apex (default: derived from --base-domain).")
	pf.StringVar(&token, "cloudflare-token", "", "Cloudflare certificate token (default $"+cloudflareEnvVar+").")

	// resolve returns the zone and whether the base domain needs a certificate at
	// all; a covered one (the apex) is a successful no-op for every subcommand.
	resolve := func() (string, bool, error) {
		z := zone
		if z == "" {
			var err error
			if z, err = fluxcore.DNSZone(baseDomain); err != nil {
				return "", false, err
			}
		}
		return z, edgecert.Required(baseDomain, z), nil
	}

	var timeout float64
	request := &cobra.Command{
		Use:   "request",
		Short: "Order the certificate (if absent) and wait until it is active.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			z, required, err := resolve()
			if err != nil {
				return render.Fail(err)
			}
			if !required {
				return render.JSON(map[string]any{"required": false, "base_domain": baseDomain})
			}
			api, err := certClient(token)
			if err != nil {
				return render.Fail(err)
			}
			p, err := edgecert.Request(cmd.Context(), api, z, baseDomain)
			if err != nil {
				return render.Fail(err)
			}
			p, err = edgecert.Wait(cmd.Context(), api, z, p, time.Duration(timeout*float64(time.Second)), edgeCertInterval)
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(map[string]any{"required": true, "edge_cert": p})
		},
	}
	request.Flags().Float64Var(&timeout, "timeout", edgeCertTimeout.Seconds(), "Seconds to wait for the certificate to become active.")

	verify := &cobra.Command{
		Use:   "verify",
		Short: "Fail unless an active certificate covers the base domain's hostnames.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			z, required, err := resolve()
			if err != nil {
				return render.Fail(err)
			}
			if !required {
				return render.JSON(map[string]any{"required": false, "base_domain": baseDomain})
			}
			api, err := certClient(token)
			if err != nil {
				return render.Fail(err)
			}
			p, err := edgecert.Verify(cmd.Context(), api, z, baseDomain)
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(map[string]any{"required": true, "edge_cert": p})
		},
	}

	del := &cobra.Command{
		Use:   "delete",
		Short: "Delete the certificate. Idempotent: succeeds if there is none.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			z, required, err := resolve()
			if err != nil {
				return render.Fail(err)
			}
			if !required {
				return render.JSON(map[string]any{"required": false, "deleted": 0})
			}
			api, err := certClient(token)
			if err != nil {
				return render.Fail(err)
			}
			n, err := edgecert.Delete(cmd.Context(), api, z, baseDomain)
			if err != nil {
				return render.Fail(err)
			}
			return render.JSON(map[string]any{"required": true, "deleted": n})
		},
	}

	ec.AddCommand(request, verify, del)
	return ec
}
