package cmd

import "github.com/spf13/cobra"

var inspectCmd = &cobra.Command{
	Use:   "inspect <delta-archive>",
	Short: "Print sidecar metadata and size statistics from a delta archive.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(inspectCmd)
}
