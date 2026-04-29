package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/config"
)

func newConfigValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [PATH]",
		Short: "Validate a config file.",
		Long: `Validate a single config file. PATH defaults to the resolved
config path ($DIFFAH_CONFIG > ~/.diffah/config.yaml). Exits 0 on
valid config (or missing file); exits 2 on parse errors.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runConfigValidate,
	}
}

func runConfigValidate(cmd *cobra.Command, args []string) error {
	path := config.DefaultPath()
	if len(args) == 1 {
		path = args[0]
	}
	if err := config.Validate(path); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "ok: %s\n", path)
	return nil
}
