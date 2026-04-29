package cmd

import "github.com/spf13/cobra"

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage diffah's optional YAML configuration.",
		Long: `Manage the optional ~/.diffah/config.yaml file (or whatever
$DIFFAH_CONFIG points to). The config supplies defaults for nine
widely-repeated flags; CLI flags always override config.

Subcommands:
  show      — print the resolved config
  init      — write a template
  validate  — validate a single file
`,
	}
	cmd.AddCommand(newConfigShowCommand())
	cmd.AddCommand(newConfigInitCommand())
	cmd.AddCommand(newConfigValidateCommand())
	return cmd
}

func init() { rootCmd.AddCommand(newConfigCommand()) }
