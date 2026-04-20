package cmd

import "github.com/spf13/cobra"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the diffah version.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cmd.Println(version)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
