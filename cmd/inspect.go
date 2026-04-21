package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/pkg/diff"
)

func newInspectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <delta-archive>",
		Short: "Print sidecar metadata and size statistics from a delta archive.",
		Args:  cobra.ExactArgs(1),
		RunE:  runInspect,
	}
}

func init() {
	rootCmd.AddCommand(newInspectCommand())
}

func runInspect(cmd *cobra.Command, args []string) error {
	raw, err := archive.ReadSidecar(args[0])
	if err != nil {
		return err
	}
	s, err := diff.ParseSidecar(raw)
	if err != nil {
		return err
	}
	return printSidecar(cmd.OutOrStdout(), args[0], s)
}

type sidecarStats struct {
	shipped, required                                 int64
	fullCount, patchCount                             int
	totalArchiveSize, patchArchiveSize, patchOrigSize int64
}

func collectSidecarStats(s *diff.Sidecar) sidecarStats {
	var st sidecarStats
	for _, b := range s.ShippedInDelta {
		st.shipped += b.Size
		switch b.Encoding {
		case diff.EncodingFull:
			st.fullCount++
		case diff.EncodingPatch:
			st.patchCount++
			st.patchArchiveSize += b.ArchiveSize
			st.patchOrigSize += b.Size
		}
		st.totalArchiveSize += b.ArchiveSize
	}
	for _, b := range s.RequiredFromBaseline {
		st.required += b.Size
	}
	return st
}

func printSidecar(w io.Writer, path string, s *diff.Sidecar) error {
	st := collectSidecarStats(s)
	total := st.shipped + st.required
	var savedPct float64
	if total > 0 {
		savedPct = (float64(st.required) / float64(total)) * 100
	}

	fmt.Fprintf(w, "archive: %s\n", path)
	fmt.Fprintf(w, "version: %s\n", s.Version)
	fmt.Fprintf(w, "tool: %s\n", s.Tool)
	fmt.Fprintf(w, "tool_version: %s\n", s.ToolVersion)
	fmt.Fprintf(w, "platform: %s\n", s.Platform)
	fmt.Fprintf(w, "created_at: %s\n", s.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(w, "target manifest digest: %s (%s)\n", s.Target.ManifestDigest, s.Target.MediaType)
	fmt.Fprintf(w, "baseline manifest digest: %s (%s)\n", s.Baseline.ManifestDigest, s.Baseline.MediaType)
	fmt.Fprintf(w, "shipped: %d blobs (%d bytes)\n", len(s.ShippedInDelta), st.shipped)
	fmt.Fprintf(w, "shipped blobs:  %d (full: %d, patch: %d)\n", len(s.ShippedInDelta), st.fullCount, st.patchCount)
	if st.patchCount > 0 && st.patchOrigSize > 0 {
		avgRatio := float64(st.patchArchiveSize) / float64(st.patchOrigSize) * 100
		fmt.Fprintf(w, "avg patch ratio: %.1f%%\n", avgRatio)
	}
	fmt.Fprintf(w, "total archive: %d bytes\n", st.totalArchiveSize)
	if st.patchCount > 0 {
		savings := st.patchOrigSize - st.patchArchiveSize
		savingsPct := float64(savings) / float64(st.patchOrigSize) * 100
		fmt.Fprintf(w, "patch savings: %d bytes (%.1f%% vs full)\n", savings, savingsPct)
	}
	fmt.Fprintf(w, "required: %d blobs (%d bytes)\n", len(s.RequiredFromBaseline), st.required)
	fmt.Fprintf(w, "saved %.1f%% vs full image\n", savedPct)
	return nil
}
