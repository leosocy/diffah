// Package exporter computes a layer-level delta archive between one or
// more (baseline, target) image pairs.
//
// The high-level entry points are [Export] and [DryRun]. Both consume
// an [Options] value carrying the per-invocation configuration:
//
//   - Pairs           — one [Pair] per image to include.
//   - Platform        — the os/arch[/variant] selector for manifest lists.
//   - OutputPath      — filesystem path for the produced archive.
//   - Workers,
//     Candidates,
//     ZstdLevel,
//     ZstdWindowLog   — Phase 4 encoder tunables. Zero values map to
//     historical Phase-3 defaults so library users that do not set them
//     keep byte-identical output across releases.
//   - SystemContext,
//     RetryTimes,
//     RetryDelay      — registry / transport configuration threaded
//     into every go.podman.io/image/v5 call.
//   - SignKeyPath,
//     SignKeyPassphrase,
//     RekorURL        — when SignKeyPath is non-empty, [Export]
//     additionally writes cosign-compatible .sig (and optionally
//     .rekor.json) sidecars next to OutputPath.
//
// For a fixed (baseline, target, Candidates, ZstdLevel, ZstdWindowLog)
// tuple the produced archive is byte-identical regardless of Workers —
// pinned by TestExport_OutputIsByteIdenticalAcrossWorkerCounts.
//
// Library users typically prefer this package's high-level entry
// points to invoking the diffah binary; the CLI is a thin wrapper
// around [Export] and [DryRun].
package exporter
