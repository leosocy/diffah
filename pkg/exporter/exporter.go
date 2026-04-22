package exporter

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/leosocy/diffah/internal/zstdpatch"
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

	Probe   Probe
	WarnOut io.Writer

	fingerprinter Fingerprinter
}

func (o *Options) defaultedProbe() Probe {
	if o.Probe != nil {
		return o.Probe
	}
	return func(ctx context.Context) (bool, string) {
		return zstdpatch.Available(ctx)
	}
}

func (o *Options) defaultedWarnOut() io.Writer {
	if o.WarnOut != nil {
		return o.WarnOut
	}
	return os.Stderr
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

type builtBundle struct {
	plans []*pairPlan
	pool  *blobPool
}

func buildBundle(ctx context.Context, opts *Options) (*builtBundle, error) {
	if err := ValidatePairs(opts.Pairs); err != nil {
		return nil, err
	}
	effectiveMode, err := resolveMode(
		ctx, opts.IntraLayer, opts.defaultedProbe(), opts.defaultedWarnOut())
	if err != nil {
		return nil, err
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
			return nil, fmt.Errorf("plan pair %q: %w", p.Name, err)
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

	if err := encodeShipped(ctx, pool, plans, effectiveMode, opts.fingerprinter, opts.Progress); err != nil {
		return nil, fmt.Errorf("encode shipped layers: %w", err)
	}
	if opts.Progress != nil {
		fmt.Fprintf(opts.Progress, "encoded %d blobs\n", len(pool.entries))
	}
	return &builtBundle{plans: plans, pool: pool}, nil
}

func Export(ctx context.Context, opts Options) error {
	bb, err := buildBundle(ctx, &opts)
	if err != nil {
		return err
	}
	sidecar := assembleSidecar(bb.pool, bb.plans, opts.Platform, opts.ToolVersion, opts.CreatedAt)
	if err := writeBundleArchive(opts.OutputPath, sidecar, bb.pool); err != nil {
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
	bb, err := buildBundle(ctx, &opts)
	if err != nil {
		return DryRunStats{}, err
	}
	sidecar := assembleSidecar(bb.pool, bb.plans, opts.Platform, opts.ToolVersion, opts.CreatedAt)
	stats := DryRunStats{
		TotalBlobs:  len(sidecar.Blobs),
		TotalImages: len(sidecar.Images),
	}
	for _, e := range sidecar.Blobs {
		stats.ArchiveSize += e.ArchiveSize
	}
	for _, plan := range bb.plans {
		var imgSize int64
		var shippedCount int
		for _, s := range plan.Shipped {
			if e, ok := bb.pool.entries[s.Digest]; ok {
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
