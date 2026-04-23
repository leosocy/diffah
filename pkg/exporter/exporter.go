package exporter

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/progress"
)

type Options struct {
	Pairs       []Pair
	Platform    string
	Compress    string
	OutputPath  string
	ToolVersion string
	IntraLayer  string
	CreatedAt   time.Time

	ProgressReporter progress.Reporter
	Progress         io.Writer

	Probe Probe

	fingerprinter Fingerprinter
}

func (o *Options) reporter() progress.Reporter {
	if o.ProgressReporter != nil {
		return o.ProgressReporter
	}
	return progress.FromWriter(o.Progress)
}

func (o *Options) defaultedProbe() Probe {
	if o.Probe != nil {
		return o.Probe
	}
	return zstdpatch.Available
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
		ctx, opts.IntraLayer, opts.defaultedProbe())
	if err != nil {
		return nil, err
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = time.Now().UTC()
	}
	log().InfoContext(ctx, "planning pairs", "count", len(opts.Pairs))
	opts.reporter().Phase("planning")

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
	log().InfoContext(ctx, "planned pairs", "count", len(plans))

	if err := encodeShipped(ctx, pool, plans, effectiveMode, opts.fingerprinter, opts.reporter()); err != nil {
		return nil, fmt.Errorf("encode shipped layers: %w", err)
	}
	log().InfoContext(ctx, "encoded blobs", "count", len(pool.entries))
	opts.reporter().Phase("encoded")
	return &builtBundle{plans: plans, pool: pool}, nil
}

func Export(ctx context.Context, opts Options) error {
	defer opts.reporter().Finish()
	bb, err := buildBundle(ctx, &opts)
	if err != nil {
		return err
	}
	sidecar := assembleSidecar(bb.pool, bb.plans, opts.Platform, opts.ToolVersion, opts.CreatedAt)
	if err := writeBundleArchive(opts.OutputPath, sidecar, bb.pool); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	var archiveSize int64
	for _, e := range sidecar.Blobs {
		archiveSize += e.ArchiveSize
	}
	log().InfoContext(ctx, "exported bundle", "path", opts.OutputPath, "archive_bytes", archiveSize)
	opts.reporter().Phase("done")
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
