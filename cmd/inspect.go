package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/zstdpatch"
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
		var p1 *diff.ErrPhase1Archive
		if errors.As(err, &p1) {
			fmt.Fprintln(cmd.OutOrStdout(), "This archive uses the Phase 1 (single-image) schema.")
			fmt.Fprintln(cmd.OutOrStdout(), "Re-export with the current diffah to use the bundle format.")
			return nil
		}
		return err
	}
	requiresZstd := s.RequiresZstd()
	zstdAvailable, _ := zstdpatch.Available(cmd.Context())
	return printBundleSidecar(cmd.OutOrStdout(), args[0], s, requiresZstd, zstdAvailable)
}

type bundleStats struct {
	fullCount, patchCount                             int
	totalArchiveSize, patchArchiveSize, patchOrigSize int64
}

func collectBundleStats(s *diff.Sidecar) bundleStats {
	var bs bundleStats
	for _, b := range s.Blobs {
		bs.totalArchiveSize += b.ArchiveSize
		switch b.Encoding {
		case diff.EncodingFull:
			bs.fullCount++
		case diff.EncodingPatch:
			bs.patchCount++
			bs.patchArchiveSize += b.ArchiveSize
			bs.patchOrigSize += b.Size
		}
	}
	return bs
}

func printBundleSidecar(w io.Writer, path string, s *diff.Sidecar, requiresZstd, zstdAvailable bool) error {
	bs := collectBundleStats(s)

	fmt.Fprintf(w, "archive: %s\n", path)
	fmt.Fprintf(w, "version: %s\n", s.Version)
	fmt.Fprintf(w, "feature: %s\n", s.Feature)
	fmt.Fprintf(w, "tool: %s\n", s.Tool)
	fmt.Fprintf(w, "tool_version: %s\n", s.ToolVersion)
	fmt.Fprintf(w, "platform: %s\n", s.Platform)
	fmt.Fprintf(w, "created_at: %s\n", s.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(w, "images: %d\n", len(s.Images))
	fmt.Fprintf(w, "blobs: %d (full: %d, patch: %d)\n", len(s.Blobs), bs.fullCount, bs.patchCount)
	if bs.patchCount > 0 && bs.patchOrigSize > 0 {
		avgRatio := float64(bs.patchArchiveSize) / float64(bs.patchOrigSize) * 100
		fmt.Fprintf(w, "avg patch ratio: %.1f%%\n", avgRatio)
	}
	fmt.Fprintf(w, "total archive: %d bytes\n", bs.totalArchiveSize)
	fmt.Fprintf(w, "intra-layer patches required: %s\n", yesNo(requiresZstd))
	fmt.Fprintf(w, "zstd available: %s\n", yesNo(zstdAvailable))
	if bs.patchCount > 0 {
		savings := bs.patchOrigSize - bs.patchArchiveSize
		savingsPct := float64(savings) / float64(bs.patchOrigSize) * 100
		fmt.Fprintf(w, "patch savings: %d bytes (%.1f%% vs full)\n", savings, savingsPct)
	}

	for _, img := range s.Images {
		fmt.Fprintf(w, "\n--- image: %s ---\n", img.Name)
		fmt.Fprintf(w, "  target manifest digest: %s (%s)\n", img.Target.ManifestDigest, img.Target.MediaType)
		fmt.Fprintf(w, "  baseline manifest digest: %s (%s)\n", img.Baseline.ManifestDigest, img.Baseline.MediaType)
		if img.Baseline.SourceHint != "" {
			fmt.Fprintf(w, "  baseline source: %s\n", img.Baseline.SourceHint)
		}
	}
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
