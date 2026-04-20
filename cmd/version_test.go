package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVersionCommand_PrintsVersion(t *testing.T) {
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"version"})

	original := version
	t.Cleanup(func() { version = original })
	version = "v0.1.0-test"

	require.NoError(t, rootCmd.Execute())
	require.Contains(t, buf.String(), "v0.1.0-test")
}
