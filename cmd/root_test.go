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
