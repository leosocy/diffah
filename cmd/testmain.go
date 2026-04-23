package cmd

import (
	"io"
	"os"
)

func Run(stdout, stderr io.Writer, args ...string) int {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	defer rootCmd.SetArgs(nil)
	return Execute(stderr)
}
