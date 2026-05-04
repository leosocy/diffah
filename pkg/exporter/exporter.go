package exporter

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/progress"
	"github.com/leosocy/diffah/pkg/signer"
)

type Options struct {
	Pairs       []Pair
	Platform    string
	Compress    string
	OutputPath  string
	ToolVersion string
	IntraLayer  string
	CreatedAt   time.Time

	// Phase 4 tunables. Zero values map to historical defaults so
	// callers that do not set them keep Phase-3 byte-identical output.
	Workers       int // 0 → 1 (serial). PR-3 changes default to 8.
	Candidates    int // 0 → 1 (single best). PR-2 changes default to 3.
	ZstdLevel     int // 0 → 3. PR-4 changes default to 22.
	ZstdWindowLog int // 0 → 27. PR-4 changes default to "auto" (per-layer).

	// Streaming I/O — Phase 4. Workdir is the spool root for
	// disk-backed baseline / target / output blob spills. Empty selects
	// the default placement; see resolveWorkdir for precedence.
	// MemoryBudget caps concurrent encoder RSS via the admission
	// controller (spec §4.3). Zero disables admission entirely
	// (operator opt-out for benchmarking). The CLI default 8GiB is
	// applied at flag-parse time, not here, so a zero-valued field
	// reaching Export() is an explicit opt-out, not an unset value.
	Workdir      string
	MemoryBudget int64

	// Registry & transport — threaded into every types.ImageReference
	// call. Nil is acceptable; it behaves the same as today's path-only
	// flow.
	SystemContext *types.SystemContext
	RetryTimes    int
	RetryDelay    time.Duration

	// Signing. A zero SignKeyPath skips signing altogether; the archive
	// is written without a sidecar. SignKeyPassphrase is consumed
	// (zeroed in place) by signer.Sign after the key is decrypted.
	SignKeyPath       string
	SignKeyPassphrase []byte
	RekorURL          string

	ProgressReporter progress.Reporter
	// Deprecated: use ProgressReporter. Will be removed in v0.4.
	Progress io.Writer

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
	pool := newBlobPool(filepath.Join(opts.Workdir, "blobs"))

	for _, p := range opts.Pairs {
		plan, err := planPair(ctx, p, opts)
		if err != nil {
			return nil, fmt.Errorf("plan pair %q: %w", p.Name, err)
		}
		plans = append(plans, plan)
		if err := seedManifestAndConfig(pool, plan); err != nil {
			return nil, fmt.Errorf("seed manifest/config %q: %w", p.Name, err)
		}
	}
	for _, plan := range plans {
		for _, s := range plan.Shipped {
			pool.countShipped(s.Digest)
		}
	}
	log().InfoContext(ctx, "planned pairs", "count", len(plans))

	if err := encodeShipped(ctx, pool, plans, effectiveMode, opts.fingerprinter, opts.reporter(),
		opts.ZstdLevel, opts.ZstdWindowLog, opts.Candidates, opts.Workers, opts.Workdir); err != nil {
		return nil, fmt.Errorf("encode shipped layers: %w", err)
	}
	log().InfoContext(ctx, "encoded blobs", "count", len(pool.entries))
	opts.reporter().Phase("encoded")
	return &builtBundle{plans: plans, pool: pool}, nil
}

func Export(ctx context.Context, opts Options) error {
	defer opts.reporter().Finish()

	wd, cleanupWorkdir, err := ensureWorkdir(opts.Workdir, opts.OutputPath)
	if err != nil {
		return fmt.Errorf("prepare workdir: %w", err)
	}
	defer cleanupWorkdir()
	opts.Workdir = wd // canonicalize for downstream consumers (PRs 3-6)

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
	if opts.SignKeyPath != "" {
		if err := signArchive(ctx, &opts); err != nil {
			return err
		}
	}
	opts.reporter().Phase("done")
	return nil
}

// signArchive re-reads diffah.json from the just-written archive,
// canonicalizes it, hashes it, and emits the three cosign-format
// sidecar files alongside the output. Called only when SignKeyPath is
// set.
func signArchive(ctx context.Context, opts *Options) error {
	sidecarBytes, err := archive.ReadSidecar(opts.OutputPath)
	if err != nil {
		return err
	}
	digest, err := signer.PayloadDigestFromSidecar(sidecarBytes)
	if err != nil {
		return err
	}

	opts.reporter().Phase("signing")
	sig, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath:         opts.SignKeyPath,
		PassphraseBytes: opts.SignKeyPassphrase,
		RekorURL:        opts.RekorURL,
		Payload:         digest[:],
	})
	if err != nil {
		return err
	}
	return signer.WriteSidecars(opts.OutputPath, sig)
}

func DryRun(ctx context.Context, opts Options) (DryRunStats, error) {
	// DryRun needs a workdir for the baseline spool, same as Export.
	// resolveWorkdir colocates with opts.OutputPath when set, and falls
	// back to os.TempDir when OutputPath is empty (API callers).
	wd, cleanup, err := ensureWorkdir(opts.Workdir, opts.OutputPath)
	if err != nil {
		return DryRunStats{}, fmt.Errorf("prepare workdir: %w", err)
	}
	defer cleanup()
	opts.Workdir = wd

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
