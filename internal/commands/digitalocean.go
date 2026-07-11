package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	doclient "github.com/dantofa/platform/internal/clients/digitalocean"
	docore "github.com/dantofa/platform/internal/core/digitalocean"
	"github.com/dantofa/platform/internal/render"
)

// newDOCmd builds the `do` resource group (aliased `digitalocean` for shells/CI
// where `do` is a reserved word; the alias is not listed separately).
func newDOCmd() *cobra.Command {
	do := &cobra.Command{
		Use:          "do",
		Aliases:      []string{"digitalocean"},
		Short:        "Manage DigitalOcean resources.",
		SilenceUsage: true,
	}
	do.AddCommand(newClusterCmd())
	do.AddCommand(newSpaceCmd())
	return do
}

// withSpaces builds a Spaces client (minting an ephemeral credential when no
// standing keys are set), runs fn, and always revokes any ephemeral key — so it
// is removed even when the operation fails.
func withSpaces(cmd *cobra.Command, region, token string, fn func(ctx context.Context, client *doclient.SpacesClient) error) error {
	ctx := cmd.Context()
	client, err := doclient.NewSpacesClient(ctx, region, token)
	if err != nil {
		render.Error(err)
		return errHandled
	}
	defer func() {
		if cerr := client.Close(); cerr != nil {
			fmt.Fprintln(os.Stderr, "warning: failed to revoke ephemeral Spaces key:", cerr)
		}
	}()
	if err := fn(ctx, client); err != nil {
		render.Error(err)
		return errHandled
	}
	return nil
}

func newSpaceCmd() *cobra.Command {
	var region, token string
	space := &cobra.Command{
		Use:   "space",
		Short: "Manage DigitalOcean Spaces buckets.",
	}
	pf := space.PersistentFlags()
	pf.StringVar(&region, "region", "",
		"Spaces region slug, e.g. nyc3 (defaults to $DIGITALOCEAN_SPACES_REGION).")
	pf.StringVar(&token, "token", "",
		"DigitalOcean API token for minting an ephemeral Spaces key when no "+
			"standing keys are set (defaults to $DIGITALOCEAN_ACCESS_TOKEN).")

	list := &cobra.Command{
		Use:   "list",
		Short: "List all Spaces buckets.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withSpaces(cmd, region, token, func(ctx context.Context, client *doclient.SpacesClient) error {
				buckets, err := docore.ListBuckets(ctx, client)
				if err != nil {
					return err
				}
				return render.JSON(buckets)
			})
		},
	}

	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a bucket.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSpaces(cmd, region, token, func(ctx context.Context, client *doclient.SpacesClient) error {
				if err := docore.CreateBucket(ctx, client, args[0]); err != nil {
					return err
				}
				return render.JSON(map[string]string{"created": args[0]})
			})
		},
	}

	del := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a bucket (must be empty).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSpaces(cmd, region, token, func(ctx context.Context, client *doclient.SpacesClient) error {
				if err := docore.DeleteBucket(ctx, client, args[0]); err != nil {
					return err
				}
				return render.JSON(map[string]string{"deleted": args[0]})
			})
		},
	}

	space.AddCommand(list, create, del)
	return space
}
