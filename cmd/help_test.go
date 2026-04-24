package cmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestInstallUsageTemplate_RendersArgumentsSection(t *testing.T) {
	cmd := &cobra.Command{
		Use:   "diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT",
		Short: "Compute a single-image delta.",
		Annotations: map[string]string{
			"arguments": "  BASELINE-IMAGE   old image to diff against (transport:path)\n" +
				"  TARGET-IMAGE     new image to diff against (transport:path)\n" +
				"  DELTA-OUT        filesystem path to write the delta archive",
		},
		Example: "  diffah diff docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar",
		Run:     func(*cobra.Command, []string) {},
	}
	installUsageTemplate(cmd)

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.Usage())

	out := buf.String()
	require.Contains(t, out, "Arguments:")
	require.Contains(t, out, "BASELINE-IMAGE   old image to diff against")
	require.Contains(t, out, "TARGET-IMAGE     new image to diff against")
	require.Contains(t, out, "DELTA-OUT        filesystem path to write")
	require.Contains(t, out, "Examples:")
	require.Less(t, bytes.Index(buf.Bytes(), []byte("Arguments:")),
		bytes.Index(buf.Bytes(), []byte("Examples:")))
}

func TestInstallUsageTemplate_OmitsArgumentsWhenAnnotationMissing(t *testing.T) {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version.",
		Run:   func(*cobra.Command, []string) {},
	}
	installUsageTemplate(cmd)

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.Usage())
	require.NotContains(t, buf.String(), "Arguments:")
}
