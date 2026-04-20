// Package diff defines the domain types and contracts shared by the
// exporter and importer services. It depends only on the standard library
// and on stable container-spec types (digest, BlobInfo).
package diff

import "fmt"

// ErrManifestListUnselected is returned when the caller passes a manifest
// list but does not specify --platform to select an instance.
type ErrManifestListUnselected struct{ Ref string }

func (e *ErrManifestListUnselected) Error() string {
	return fmt.Sprintf("image %q is a manifest list: re-run with --platform os/arch[/variant]", e.Ref)
}

// ErrUnsupportedSchemaVersion is returned when a sidecar's version is not
// recognized by the current reader.
type ErrUnsupportedSchemaVersion struct{ Got string }

func (e *ErrUnsupportedSchemaVersion) Error() string {
	return fmt.Sprintf("unsupported sidecar version %q (this build supports v1)", e.Got)
}

// ErrSidecarSchema wraps a sidecar JSON decoding or validation failure.
type ErrSidecarSchema struct{ Reason string }

func (e *ErrSidecarSchema) Error() string {
	return fmt.Sprintf("sidecar schema violation: %s", e.Reason)
}

// ErrBaselineMissingBlob is returned when a digest that the delta requires
// from baseline cannot be resolved against the provided baseline source.
type ErrBaselineMissingBlob struct{ Digest, Source string }

func (e *ErrBaselineMissingBlob) Error() string {
	return fmt.Sprintf("baseline %q does not provide required blob %s", e.Source, e.Digest)
}

// ErrIncompatibleOutputFormat is returned when the requested --output-format
// conflicts with the source manifest media type in a way diffah cannot
// reconcile without explicit user intent.
type ErrIncompatibleOutputFormat struct{ SourceMime, OutputFormat string }

func (e *ErrIncompatibleOutputFormat) Error() string {
	return fmt.Sprintf("source media type %q incompatible with output format %q",
		e.SourceMime, e.OutputFormat)
}

// ErrSourceManifestUnreadable is returned when the target manifest cannot be
// fetched or parsed.
type ErrSourceManifestUnreadable struct {
	Ref   string
	Cause error
}

func (e *ErrSourceManifestUnreadable) Error() string {
	return fmt.Sprintf("cannot read manifest of %q: %v", e.Ref, e.Cause)
}

func (e *ErrSourceManifestUnreadable) Unwrap() error { return e.Cause }

// ErrDigestMismatch is returned when a post-operation verification detects
// that the resulting digest does not match the expected one.
type ErrDigestMismatch struct{ Where, Want, Got string }

func (e *ErrDigestMismatch) Error() string {
	return fmt.Sprintf("%s: digest mismatch: want %s, got %s", e.Where, e.Want, e.Got)
}

// ErrIntraLayerAssemblyMismatch reports that a patched layer's computed
// sha256 did not match the manifest-declared digest. Import must fail fast
// with no partial output.
type ErrIntraLayerAssemblyMismatch struct{ Digest, Got string }

func (e *ErrIntraLayerAssemblyMismatch) Error() string {
	return fmt.Sprintf("intra-layer assembly mismatch: expected %s, got %s",
		e.Digest, e.Got)
}

// ErrBaselineMissingPatchRef is the patch-specific sibling of
// ErrBaselineMissingBlob. Raised when a shipped layer with encoding=patch
// names a patch_from_digest that is absent from the provided baseline.
type ErrBaselineMissingPatchRef struct{ Digest, Source string }

func (e *ErrBaselineMissingPatchRef) Error() string {
	return fmt.Sprintf("baseline %q does not provide patch reference blob %s",
		e.Source, e.Digest)
}

// ErrIntraLayerUnsupported is raised on the exporter side when the current
// options make intra-layer mode impossible (e.g. baseline is manifest-only
// with no blob bytes).
type ErrIntraLayerUnsupported struct{ Reason string }

func (e *ErrIntraLayerUnsupported) Error() string {
	return fmt.Sprintf("intra-layer mode unsupported: %s", e.Reason)
}
