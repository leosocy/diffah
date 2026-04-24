package cmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestRequireArgs_TooFew(t *testing.T) {
	cmd := &cobra.Command{Use: "diff"}
	validator := requireArgs("diff",
		[]string{"BASELINE-IMAGE", "TARGET-IMAGE", "DELTA-OUT"},
		"diffah diff docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar",
	)
	err := validator(cmd, []string{"docker-archive:/tmp/x.tar"})
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "'diff' requires 3 arguments")
	require.Contains(t, msg, "BASELINE-IMAGE, TARGET-IMAGE, DELTA-OUT")
	require.Contains(t, msg, "got 1")
	require.Contains(t, msg, "Usage:\n  diffah diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT")
	require.Contains(t, msg, "Example:\n  diffah diff docker-archive:")
	require.Contains(t, msg, "Run 'diffah diff --help'")
}

func TestRequireArgs_TooMany(t *testing.T) {
	cmd := &cobra.Command{Use: "apply"}
	validator := requireArgs("apply",
		[]string{"DELTA-IN", "BASELINE-IMAGE", "TARGET-OUT"},
		"diffah apply delta.tar docker-archive:/tmp/old.tar docker-archive:/tmp/out.tar",
	)
	err := validator(cmd, []string{"a", "b", "c", "d"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "got 4")
}

func TestRequireArgs_ExactCount(t *testing.T) {
	cmd := &cobra.Command{Use: "diff"}
	validator := requireArgs("diff",
		[]string{"BASELINE-IMAGE", "TARGET-IMAGE", "DELTA-OUT"},
		"diffah diff ...",
	)
	err := validator(cmd, []string{"a", "b", "c"})
	require.NoError(t, err)
}

func TestRequireArgs_ErrorIsCategoryUser(t *testing.T) {
	cmd := &cobra.Command{Use: "diff"}
	validator := requireArgs("diff",
		[]string{"BASELINE-IMAGE", "TARGET-IMAGE", "DELTA-OUT"},
		"diffah diff ...",
	)
	err := validator(cmd, []string{})
	require.Error(t, err)
	var buf bytes.Buffer
	code := classifyAndExit(&buf, err, "text")
	require.Equal(t, 2, code)
}
