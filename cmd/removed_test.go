package cmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

func TestRemovedCommand_ExportRedirects(t *testing.T) {
	root := &cobra.Command{Use: "diffah", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newRemovedExport())

	var stderr bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&stderr)
	root.SetArgs([]string{"export", "--pair", "app=v1.tar,v2.tar", "bundle.tar"})

	code := classifyAndExit(&stderr, root.Execute(), "text")
	require.Equal(t, 2, code)
	out := stderr.String()
	require.Contains(t, out, "unknown command 'export'")
	require.Contains(t, out, "was removed in the CLI redesign")
	require.Contains(t, out, "diffah diff")
	require.Contains(t, out, "BASELINE-IMAGE TARGET-IMAGE DELTA-OUT")
	require.Contains(t, out, "diffah bundle")
	require.Contains(t, out, "BUNDLE-SPEC")
	require.Contains(t, out, "diffah --help")
}

func TestRemovedCommand_ImportRedirects(t *testing.T) {
	root := &cobra.Command{Use: "diffah", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newRemovedImport())

	var stderr bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&stderr)
	root.SetArgs([]string{"import", "--baseline", "default=v1.tar", "--delta", "d.tar", "o.tar"})

	code := classifyAndExit(&stderr, root.Execute(), "text")
	require.Equal(t, 2, code)
	out := stderr.String()
	require.Contains(t, out, "unknown command 'import'")
	require.Contains(t, out, "diffah apply")
	require.Contains(t, out, "DELTA-IN BASELINE-IMAGE TARGET-IMAGE")
	require.Contains(t, out, "diffah unbundle")
	require.Contains(t, out, "BASELINE-SPEC")
}

func TestRemovedCommand_HiddenFromHelp(t *testing.T) {
	exportCmd := newRemovedExport()
	importCmd := newRemovedImport()
	require.True(t, exportCmd.Hidden, "removed export should be hidden")
	require.True(t, importCmd.Hidden, "removed import should be hidden")
}

func TestRemovedCommand_DisableFlagParsing(t *testing.T) {
	exportCmd := newRemovedExport()
	importCmd := newRemovedImport()
	require.True(t, exportCmd.DisableFlagParsing, "removed export should disable flag parsing")
	require.True(t, importCmd.DisableFlagParsing, "removed import should disable flag parsing")
}

func TestRemovedErr_ClassifyReturnsUserWithHint(t *testing.T) {
	err := removedErr("export", []removedReplacement{
		{verb: "diff", args: "BASELINE-IMAGE TARGET-IMAGE DELTA-OUT", note: "single-image delta"},
	})
	cat, hint := errs.Classify(err)
	require.Equal(t, errs.CategoryUser, cat)
	require.Contains(t, hint, "diffah --help")
}
