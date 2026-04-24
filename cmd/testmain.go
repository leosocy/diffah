package cmd

import (
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func Run(stdout, stderr io.Writer, args ...string) int {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	resetFlagsRecursive(rootCmd)
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	defer rootCmd.SetArgs(nil)
	return Execute(stderr)
}

// resetFlagsRecursive restores every flag on cmd and its subcommands to its
// registered default. Cobra does not clear flag state between Execute calls,
// so stale values (notably the per-subcommand --help bool flipped by a prior
// help invocation) would leak across tests that share rootCmd. Resetting
// before SetArgs keeps the PersistentPreRunE hook reading args-scoped values.
func resetFlagsRecursive(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
	for _, sub := range cmd.Commands() {
		resetFlagsRecursive(sub)
	}
}
