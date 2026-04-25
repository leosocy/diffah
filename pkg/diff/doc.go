// Package diff defines the on-disk delta archive sidecar schema and the
// pure data types shared by the exporter and importer.
//
// The package is intentionally free of I/O and framework dependencies:
// it can be vendored by a third-party tool that only needs to read or
// generate diffah-compatible sidecars.
//
// Key types:
//
//   - [Sidecar]       — the parsed form of diffah.json (schema "v1").
//   - [BlobRef]       — canonical layer / config blob description.
//   - [Encoding]      — discriminator for shipped blobs ("full" or
//     "patch"); see [EncodingFull] and [EncodingPatch].
//   - [BundleSpec]    — JSON spec consumed by `diffah bundle`.
//   - [BaselineSpec]  — JSON spec consumed by `diffah unbundle`.
//   - [OutputSpec]    — JSON spec consumed by `diffah unbundle`.
//
// Forward-compat rules for the sidecar schema and the registry-error
// classification surface in this package are documented in
// docs/compat.md at the repository root.
package diff
