package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/progress"
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

func TestClassifyAndExit_UserError(t *testing.T) {
	var buf bytes.Buffer
	require.Equal(t, 2, classifyAndExit(&buf, &diff.ErrBaselineMismatch{Name: "x"}, "text"))
}

func TestClassifyAndExit_ContentError(t *testing.T) {
	var buf bytes.Buffer
	got := classifyAndExit(&buf, &diff.ErrDigestMismatch{Where: "blob", Want: "sha256:aa", Got: "sha256:bb"}, "text")
	require.Equal(t, 4, got)
}

func TestClassifyAndExit_NilError(t *testing.T) {
	var buf bytes.Buffer
	require.Equal(t, 0, classifyAndExit(&buf, nil, "text"))
	require.Empty(t, buf.String())
}

func TestClassifyAndExit_UnknownError(t *testing.T) {
	var buf bytes.Buffer
	require.Equal(t, 1, classifyAndExit(&buf, errors.New("mysterious"), "text"))
}

func TestClassifyAndExit_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	classifyAndExit(&buf, &diff.ErrBaselineMismatch{Name: "x"}, "text")
	require.Contains(t, buf.String(), "diffah: user:")
	require.Contains(t, buf.String(), "hint:")
}

func TestClassifyAndExit_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	classifyAndExit(&buf, &diff.ErrBaselineMismatch{Name: "x"}, "json")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	require.Equal(t, float64(1), parsed["schema_version"])
	errData := parsed["error"].(map[string]any)
	require.Equal(t, "user", errData["category"])
	require.Equal(t, "the supplied baseline has the wrong manifest digest", errData["next_action"])
}

type fakeSlogReporter struct{ buf *bytes.Buffer }

func (fakeSlogReporter) Phase(string) {}
func (fakeSlogReporter) StartLayer(digest.Digest, int64, string) progress.Layer {
	return nil
}
func (fakeSlogReporter) Finish()                 {}
func (r fakeSlogReporter) SlogWriter() io.Writer { return r.buf }

func withLoggerDefaults(t *testing.T, level, format string) {
	t.Helper()
	prev := slog.Default()
	prevLevel, prevFormat := logLevel, logFormat
	t.Cleanup(func() {
		slog.SetDefault(prev)
		logLevel, logFormat = prevLevel, prevFormat
	})
	logLevel, logFormat = level, format
}

func TestRewireSlogToBars_AutoFormatHonorsTTY(t *testing.T) {
	withLoggerDefaults(t, "info", "auto")

	var buf bytes.Buffer
	rewireSlogToBars(fakeSlogReporter{buf: &buf}, true)
	slog.Default().Info("hello", "k", "v")

	if strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("auto+TTY expected text handler, got JSON: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("expected hello in output, got %q", buf.String())
	}
}

func TestRewireSlogToBars_AutoFormatNonTTYPicksJSON(t *testing.T) {
	withLoggerDefaults(t, "info", "auto")

	var buf bytes.Buffer
	rewireSlogToBars(fakeSlogReporter{buf: &buf}, false)
	slog.Default().Info("hello")

	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("auto+non-TTY expected JSON handler, got %q", buf.String())
	}
}
