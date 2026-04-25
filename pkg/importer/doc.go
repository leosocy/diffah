// Package importer reconstructs full container images by applying a
// diffah delta archive on top of one or more baseline images.
//
// The high-level entry points are [Import] and [DryRun]. Both consume
// an [Options] value carrying the per-invocation configuration:
//
//   - DeltaPath        — filesystem path to the delta archive.
//   - Baselines        — name → transport-prefixed baseline reference.
//   - Outputs          — name → transport-prefixed destination reference.
//   - Strict           — when true, every image in the bundle must
//     have a matching baseline; otherwise unmatched images are skipped
//     with a warning.
//   - SystemContext,
//     RetryTimes,
//     RetryDelay       — registry / transport configuration threaded
//     into every go.podman.io/image/v5 call.
//   - AllowConvert     — opt in to manifest media-type conversion when
//     the source and destination disagree (changes every digest).
//   - VerifyPubKeyPath,
//     VerifyRekorURL   — when VerifyPubKeyPath is non-empty, [Import]
//     reads .sig (and optionally .rekor.json) sidecars next to
//     DeltaPath and rejects archives whose signature does not match
//     the supplied PEM-encoded ECDSA-P256 public key.
//
// When a baseline reference uses the docker:// transport, only the
// blobs referenced by patch-encoded entries in the delta cross the
// wire (lazy fetch). All other baseline blobs are reused from the
// baseline image source as-is.
//
// Library users typically prefer this package's high-level entry
// points to invoking the diffah binary; the CLI is a thin wrapper
// around [Import] and [DryRun].
package importer
