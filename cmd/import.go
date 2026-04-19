package cmd

import "github.com/spf13/cobra"

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Reconstruct a full image from a delta archive and a baseline source.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(importCmd)
}
