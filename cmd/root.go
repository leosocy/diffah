// Package cmd contains the cobra command tree for the diffah CLI.
package cmd

import (
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags. It defaults to "dev" for
// developer builds.
var version = "dev"

var logLevel string

var rootCmd = &cobra.Command{
	Use:   "diffah",
	Short: "Produce and apply portable container-image layer-diff archives.",
	Long: `diffah computes a layer-level diff between two container images,
packages the new layers into a portable archive, and reconstructs the
full target image from any baseline source on the consuming side.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command and returns any error encountered.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info",
		"log level: debug|info|warn|error")
}
