package cmd

import "github.com/spf13/cobra"

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export a layer-diff delta archive from baseline and target images.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(exportCmd)
}
