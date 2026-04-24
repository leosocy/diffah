package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiffCommand_HelpShowsArgumentsAndExamples(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "diff", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "Usage:")
	require.Contains(t, out, "diffah diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT")
	require.Contains(t, out, "Arguments:")
	require.Contains(t, out, "BASELINE-IMAGE")
	require.Contains(t, out, "TARGET-IMAGE")
	require.Contains(t, out, "DELTA-OUT")
	require.Contains(t, out, "Examples:")
	require.Contains(t, out, "docker-archive:/")
}

func TestDiffCommand_RejectsWrongArgCount(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "diff", "docker-archive:/tmp/only.tar")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "'diff' requires 3 arguments")
	require.Contains(t, stderr.String(), "BASELINE-IMAGE, TARGET-IMAGE, DELTA-OUT")
	require.Contains(t, stderr.String(), "got 1")
}

func TestDiffCommand_RejectsMissingTransportPrefix(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "diff",
		"/tmp/old.tar", "/tmp/new.tar", "/tmp/delta.tar")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "missing transport prefix for BASELINE-IMAGE")
	require.Contains(t, stderr.String(), "Did you mean:  docker-archive:/tmp/old.tar")
}

func TestDiffCommand_RejectsReservedTransport(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "diff",
		"docker://registry/img:v1",
		"docker-archive:/tmp/new.tar",
		"/tmp/delta.tar")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "is reserved but not yet implemented")
}
