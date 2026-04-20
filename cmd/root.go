// Package cmd contains the cobra command tree for the diffah CLI.
package cmd

import (
	"fmt"
	"io"

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

// Execute runs the root command. On error it writes a "diffah: <msg>"
// line to stderr so operators see why a non-zero exit happened;
// SilenceErrors keeps cobra itself quiet so the message is not duplicated.
func Execute(stderr io.Writer) error {
	err := rootCmd.Execute()
	if err != nil {
		fmt.Fprintln(stderr, "diffah:", err)
	}
	return err
}

func init() {
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info",
		"log level: debug|info|warn|error")
}
