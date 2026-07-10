package commands

import (
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

func newSpaceCmd() *cobra.Command {
	var region string
	space := &cobra.Command{
		Use:   "space",
		Short: "Manage DigitalOcean Spaces buckets.",
	}
	space.PersistentFlags().StringVar(&region, "region", "",
		"Spaces region slug, e.g. nyc3 (defaults to $DIGITALOCEAN_SPACES_REGION).")

	list := &cobra.Command{
		Use:   "list",
		Short: "List all Spaces buckets.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := doclient.NewSpacesClient(region)
			if err != nil {
				render.Error(err)
				return errHandled
			}
			buckets, err := docore.ListBuckets(cmd.Context(), client)
			if err != nil {
				render.Error(err)
				return errHandled
			}
			return render.JSON(buckets)
		},
	}

	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a bucket.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := doclient.NewSpacesClient(region)
			if err != nil {
				render.Error(err)
				return errHandled
			}
			if err := docore.CreateBucket(cmd.Context(), client, args[0]); err != nil {
				render.Error(err)
				return errHandled
			}
			return render.JSON(map[string]string{"created": args[0]})
		},
	}

	del := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a bucket (must be empty).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := doclient.NewSpacesClient(region)
			if err != nil {
				render.Error(err)
				return errHandled
			}
			if err := docore.DeleteBucket(cmd.Context(), client, args[0]); err != nil {
				render.Error(err)
				return errHandled
			}
			return render.JSON(map[string]string{"deleted": args[0]})
		},
	}

	space.AddCommand(list, create, del)
	return space
}
