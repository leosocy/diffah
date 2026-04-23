package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
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
	require.True(t, names["doctor"], "doctor subcommand missing")
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

	exit := Execute(&stderr)
	require.NotEqual(t, 0, exit)
	require.Contains(t, stderr.String(), "diffah:",
		"expected diffah-prefixed error on stderr so operators can see what went wrong")
}

func TestExecute_SilentOnSuccess(t *testing.T) {
	var stderr bytes.Buffer
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"--help"})

	exit := Execute(&stderr)
	require.Equal(t, 0, exit)
	require.Empty(t, stderr.String(),
		"no error path should write to the stderr sink")
}

func TestClassifyExitCode_UserError(t *testing.T) {
	got := ClassifyExitCode(&diff.ErrBaselineMismatch{Name: "x"})
	require.Equal(t, 2, got)
}

func TestClassifyExitCode_ContentError(t *testing.T) {
	got := ClassifyExitCode(&diff.ErrDigestMismatch{Where: "blob", Want: "sha256:aa", Got: "sha256:bb"})
	require.Equal(t, 4, got)
}

func TestClassifyExitCode_NilError(t *testing.T) {
	got := ClassifyExitCode(nil)
	require.Equal(t, 0, got)
}

func TestClassifyExitCode_UnknownError(t *testing.T) {
	got := ClassifyExitCode(errors.New("mysterious"))
	require.Equal(t, 1, got)
}

func TestRenderError_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	RenderError(&buf, &diff.ErrBaselineMismatch{Name: "x"}, "text")
	require.Contains(t, buf.String(), "diffah: user:")
	require.Contains(t, buf.String(), "hint:")
}

func TestRenderError_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	RenderError(&buf, &diff.ErrBaselineMismatch{Name: "x"}, "json")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	require.Equal(t, float64(1), parsed["schema_version"])
	errData := parsed["error"].(map[string]any)
	require.Equal(t, "user", errData["category"])
	require.Equal(t, "the supplied baseline has the wrong manifest digest", errData["next_action"])
}
