package exporter

import (
	"context"
	"fmt"
	"io"
	"time"
)

type Options struct {
	Pairs       []Pair
	Platform    string
	Compress    string
	OutputPath  string
	ToolVersion string
	IntraLayer  string
	CreatedAt   time.Time
	Progress    io.Writer

	fingerprinter Fingerprinter
}

type DryRunStats struct {
	TotalBlobs  int
	TotalImages int
	ArchiveSize int64
	PerImage    []ImageStats
}

type ImageStats struct {
	Name         string
	ShippedBlobs int
	ArchiveSize  int64
}

func Export(ctx context.Context, opts Options) error {
	if err := ValidatePairs(opts.Pairs); err != nil {
		return err
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = time.Now().UTC()
	}

	if opts.Progress != nil {
		fmt.Fprintf(opts.Progress, "planning %d pairs...\n", len(opts.Pairs))
	}

	plans := make([]*pairPlan, 0, len(opts.Pairs))
	pool := newBlobPool()

	for _, p := range opts.Pairs {
		plan, err := planPair(ctx, p, opts.Platform)
		if err != nil {
			return fmt.Errorf("plan pair %q: %w", p.Name, err)
		}
		plans = append(plans, plan)
		seedManifestAndConfig(pool, plan)
	}

	for _, plan := range plans {
		for _, s := range plan.Shipped {
			pool.countShipped(s.Digest)
		}
	}

	if opts.Progress != nil {
		fmt.Fprintf(opts.Progress, "planned %d pairs\n", len(plans))
	}

	if err := encodeShipped(ctx, pool, plans, opts.IntraLayer, opts.fingerprinter); err != nil {
		return fmt.Errorf("encode shipped layers: %w", err)
	}

	if opts.Progress != nil {
		fmt.Fprintf(opts.Progress, "encoded %d blobs\n", len(pool.entries))
	}

	sidecar := assembleSidecar(pool, plans, opts.Platform, opts.ToolVersion, opts.CreatedAt)

	if err := writeBundleArchive(opts.OutputPath, sidecar, pool); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}

	if opts.Progress != nil {
		var archiveSize int64
		for _, e := range sidecar.Blobs {
			archiveSize += e.ArchiveSize
		}
		fmt.Fprintf(opts.Progress, "wrote %s (%d bytes)\n", opts.OutputPath, archiveSize)
	}

	return nil
}

func DryRun(ctx context.Context, opts Options) (DryRunStats, error) {
	if err := ValidatePairs(opts.Pairs); err != nil {
		return DryRunStats{}, err
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = time.Now().UTC()
	}

	plans := make([]*pairPlan, 0, len(opts.Pairs))
	pool := newBlobPool()

	for _, p := range opts.Pairs {
		plan, err := planPair(ctx, p, opts.Platform)
		if err != nil {
			return DryRunStats{}, fmt.Errorf("plan pair %q: %w", p.Name, err)
		}
		plans = append(plans, plan)
		seedManifestAndConfig(pool, plan)
	}

	for _, plan := range plans {
		for _, s := range plan.Shipped {
			pool.countShipped(s.Digest)
		}
	}

	if err := encodeShipped(ctx, pool, plans, opts.IntraLayer, opts.fingerprinter); err != nil {
		return DryRunStats{}, fmt.Errorf("encode shipped layers: %w", err)
	}

	sidecar := assembleSidecar(pool, plans, opts.Platform, opts.ToolVersion, opts.CreatedAt)

	stats := DryRunStats{
		TotalBlobs:  len(sidecar.Blobs),
		TotalImages: len(sidecar.Images),
	}
	for _, e := range sidecar.Blobs {
		stats.ArchiveSize += e.ArchiveSize
	}
	for _, plan := range plans {
		var imgSize int64
		var shippedCount int
		for _, s := range plan.Shipped {
			if e, ok := pool.entries[s.Digest]; ok {
				imgSize += e.ArchiveSize
				shippedCount++
			}
		}
		stats.PerImage = append(stats.PerImage, ImageStats{
			Name: plan.Name, ShippedBlobs: shippedCount, ArchiveSize: imgSize,
		})
	}
	return stats, nil
}
