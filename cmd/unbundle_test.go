package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnbundleCommand_HelpShowsArguments(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "unbundle", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "diffah unbundle DELTA-IN BASELINE-SPEC OUTPUT-DIR")
	require.Contains(t, out, "Arguments:")
	require.Contains(t, out, "DELTA-IN")
	require.Contains(t, out, "BASELINE-SPEC")
	require.Contains(t, out, "OUTPUT-DIR")
}

func TestUnbundleCommand_RejectsWrongArgCount(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "unbundle", "d.tar", "b.json")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "'unbundle' requires 3 arguments")
}

func TestUnbundleCommand_AcceptsStrict(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "unbundle", "--help")
	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "--strict")
}
