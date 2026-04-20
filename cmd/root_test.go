package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootCommand_HasExpectedSubcommands(t *testing.T) {
	names := make(map[string]bool)
	for _, c := range rootCmd.Commands() {
		names[c.Name()] = true
	}
	require.True(t, names["export"], "export subcommand missing")
	require.True(t, names["import"], "import subcommand missing")
	require.True(t, names["inspect"], "inspect subcommand missing")
	require.True(t, names["version"], "version subcommand missing")
}

func TestRootCommand_HelpListsSubcommands(t *testing.T) {
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"--help"})
	require.NoError(t, rootCmd.Execute())

	out := buf.String()
	require.Contains(t, out, "export")
	require.Contains(t, out, "import")
	require.Contains(t, out, "inspect")
}

func TestExecute_PrintsErrorToStderrOnFailure(t *testing.T) {
	var stderr bytes.Buffer
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"no-such-subcommand"})

	err := Execute(&stderr)
	require.Error(t, err)
	require.Contains(t, stderr.String(), "diffah:",
		"expected diffah-prefixed error on stderr so operators can see what went wrong; "+
			"previously main.go discarded the error, exiting 1 with empty output")
}

func TestExecute_SilentOnSuccess(t *testing.T) {
	var stderr bytes.Buffer
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"--help"})

	require.NoError(t, Execute(&stderr))
	require.Empty(t, stderr.String(),
		"no error path should write to the stderr sink")
}
