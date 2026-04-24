package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyCommand_HelpShowsArgumentsAndExamples(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "apply", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "diffah apply DELTA-IN BASELINE-IMAGE TARGET-IMAGE")
	require.Contains(t, out, "Arguments:")
	require.Contains(t, out, "DELTA-IN")
	require.Contains(t, out, "BASELINE-IMAGE")
	require.Contains(t, out, "TARGET-IMAGE")
	require.Contains(t, out, "Examples:")
}

func TestApplyCommand_RejectsWrongArgCount(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "apply", "delta.tar", "docker-archive:/tmp/old.tar")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "'apply' requires 3 arguments")
	require.Contains(t, stderr.String(), "DELTA-IN, BASELINE-IMAGE, TARGET-IMAGE")
}

func TestApplyCommand_RejectsBaselineMissingTransport(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "apply",
		"delta.tar", "/tmp/old.tar", "/tmp/out.tar")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "missing transport prefix for BASELINE-IMAGE")
}

func TestApplyCommand_RequiresTransportPrefixOnTargetImage(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "apply",
		"delta.tar", "docker-archive:/tmp/old.tar", "/tmp/restored.tar")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "missing transport prefix for TARGET-IMAGE")
}

func TestApplyCommand_RejectsImageFormatFlag(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "apply",
		"--image-format", "dir",
		"delta.tar", "docker-archive:/tmp/old.tar", "dir:/tmp/out")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "unknown flag")
}

func TestApplyCommand_RegistersRegistryFlags(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "apply", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "--authfile")
	require.Contains(t, out, "--creds")
	require.Contains(t, out, "--tls-verify")
	require.Contains(t, out, "--retry-times")
}
