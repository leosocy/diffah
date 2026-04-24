// Package diff defines the domain types and contracts shared by the
// exporter and importer services. It depends only on the standard library
// and on stable container-spec types (digest, BlobInfo).
package diff

import (
	"fmt"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// ErrManifestListUnselected is returned when the caller passes a manifest
// list but does not specify --platform to select an instance.
type ErrManifestListUnselected struct{ Ref string }

func (e *ErrManifestListUnselected) Error() string {
	return fmt.Sprintf("image %q is a manifest list: re-run with --platform os/arch[/variant]", e.Ref)
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

// ErrUnknownImageFormat is returned when the caller passes a value to the
// --image-format flag that is not one of the supported image formats
// (docker-archive, oci-archive, dir).
type ErrUnknownImageFormat struct{ Got string }

func (e *ErrUnknownImageFormat) Error() string {
	return fmt.Sprintf("unknown --image-format %q (valid: docker-archive, oci-archive, dir)", e.Got)
}

// ErrNotADiffahArchive is returned when a file that was expected to be a
// diffah delta archive is opened but does not contain the sidecar JSON.
// Classified as content because the file — if it existed and was readable —
// simply is not a diffah artifact.
type ErrNotADiffahArchive struct{ Path string }

func (e *ErrNotADiffahArchive) Error() string {
	return fmt.Sprintf("archive %s is not a diffah delta (missing %s)", e.Path, SidecarFilename)
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

// ErrBaselineBlobDigestMismatch reports that a baseline-served blob's
// computed sha256 did not match the digest the sidecar expected. Bytes
// are never written to the output when this fires.
type ErrBaselineBlobDigestMismatch struct {
	ImageName string
	Digest    string
	Got       string
}

func (e *ErrBaselineBlobDigestMismatch) Error() string {
	return fmt.Sprintf("image %q: baseline blob %s has digest %s",
		e.ImageName, e.Digest, e.Got)
}

// ErrShippedBlobDigestMismatch reports that a bundle-shipped blob's
// computed sha256 did not match the digest recorded in the sidecar.
// This indicates bundle corruption or a writer bug.
type ErrShippedBlobDigestMismatch struct {
	ImageName string
	Digest    string
	Got       string
}

func (e *ErrShippedBlobDigestMismatch) Error() string {
	return fmt.Sprintf("image %q: shipped blob %s has digest %s",
		e.ImageName, e.Digest, e.Got)
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

type ErrPhase1Archive struct{ GotFeature string }

func (e *ErrPhase1Archive) Error() string {
	if e.GotFeature == "" {
		return "archive uses Phase 1 schema (feature marker missing); re-export with the current diffah"
	}
	return fmt.Sprintf("archive uses Phase 1 schema (feature=%q); re-export with the current diffah", e.GotFeature)
}

type ErrUnknownBundleVersion struct{ Got string }

func (e *ErrUnknownBundleVersion) Error() string {
	return fmt.Sprintf("unknown bundle version %q (this build supports %q)", e.Got, SchemaVersionV1)
}

type ErrInvalidBundleFormat struct{ Cause error }

func (e *ErrInvalidBundleFormat) Error() string {
	return fmt.Sprintf("invalid bundle format: %v", e.Cause)
}

func (e *ErrInvalidBundleFormat) Unwrap() error { return e.Cause }

type ErrMultiImageNeedsNamedBaselines struct{ N int }

func (e *ErrMultiImageNeedsNamedBaselines) Error() string {
	return fmt.Sprintf(
		"archive contains %d images; multi-image unbundle requires a BASELINE-SPEC JSON"+
			" mapping each image name to its baseline path",
		e.N)
}

type ErrBaselineNameUnknown struct {
	Name      string
	Available []string
}

func (e *ErrBaselineNameUnknown) Error() string {
	return fmt.Sprintf("baseline name %q not in bundle (available: %v)", e.Name, e.Available)
}

type ErrBaselineMismatch struct{ Name, Expected, Got string }

func (e *ErrBaselineMismatch) Error() string {
	return fmt.Sprintf("wrong baseline for %q: manifest digest mismatch (expected %s, got %s)", e.Name, e.Expected, e.Got)
}

type ErrBaselineMissing struct{ Names []string }

func (e *ErrBaselineMissing) Error() string {
	return fmt.Sprintf("strict mode: missing baselines for %v", e.Names)
}

type ErrInvalidBundleSpec struct {
	Path   string
	Reason string
}

func (e *ErrInvalidBundleSpec) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("invalid bundle spec %q: %s", e.Path, e.Reason)
	}
	return fmt.Sprintf("invalid bundle spec: %s", e.Reason)
}

type ErrDuplicateBundleName struct{ Name string }

func (e *ErrDuplicateBundleName) Error() string {
	return fmt.Sprintf("duplicate bundle image name %q", e.Name)
}

// ErrRegistryAuth is returned when authentication against a registry
// fails (401/403 or an auth-config parse error). Classified as user.
type ErrRegistryAuth struct{ Registry string }

func (e *ErrRegistryAuth) Error() string {
	return fmt.Sprintf("authentication failed against registry %q", e.Registry)
}

// ErrRegistryNetwork wraps connectivity, DNS, and timeout errors
// raised while talking to a registry. Classified as environment.
type ErrRegistryNetwork struct {
	Op    string
	Cause error
}

func (e *ErrRegistryNetwork) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("registry network error during %s", e.Op)
	}
	return fmt.Sprintf("registry network error during %s: %v", e.Op, e.Cause)
}

func (e *ErrRegistryNetwork) Unwrap() error { return e.Cause }

// ErrRegistryManifestMissing is returned when a manifest request
// returns 404. Classified as content.
type ErrRegistryManifestMissing struct{ Ref string }

func (e *ErrRegistryManifestMissing) Error() string {
	return fmt.Sprintf("manifest not found at %s", e.Ref)
}

// ErrRegistryManifestInvalid is returned when a manifest body fails
// to parse or uses an unsupported schema. Classified as content.
type ErrRegistryManifestInvalid struct{ Ref, Reason string }

func (e *ErrRegistryManifestInvalid) Error() string {
	return fmt.Sprintf("invalid manifest at %s: %s", e.Ref, e.Reason)
}

func (*ErrManifestListUnselected) Category() errs.Category { return errs.CategoryUser }
func (*ErrManifestListUnselected) NextAction() string {
	return "pass --platform os/arch[/variant] to select a manifest-list instance"
}

func (*ErrSidecarSchema) Category() errs.Category { return errs.CategoryContent }
func (*ErrSidecarSchema) NextAction() string {
	return "archive may be corrupt or from an unsupported version"
}

func (*ErrBaselineMissingBlob) Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineMissingBlob) NextAction() string {
	return "verify the --baseline value matches the baseline the delta was built against"
}

func (*ErrIncompatibleOutputFormat) Category() errs.Category { return errs.CategoryUser }
func (*ErrIncompatibleOutputFormat) NextAction() string {
	return "pass --allow-convert to accept digest drift, or pick a compatible --image-format"
}

func (*ErrUnknownImageFormat) Category() errs.Category { return errs.CategoryUser }
func (*ErrUnknownImageFormat) NextAction() string {
	return "use one of: docker-archive, oci-archive, dir"
}

func (*ErrNotADiffahArchive) Category() errs.Category { return errs.CategoryContent }
func (*ErrNotADiffahArchive) NextAction() string {
	return "verify the path points at an archive produced by 'diffah diff' or 'diffah bundle'"
}

func (e *ErrSourceManifestUnreadable) Category() errs.Category {
	if e == nil || e.Cause == nil {
		return errs.CategoryEnvironment
	}
	if cat, _ := errs.Classify(e.Cause); cat != errs.CategoryInternal {
		return cat
	}
	return errs.CategoryEnvironment
}

func (*ErrDigestMismatch) Category() errs.Category             { return errs.CategoryContent }
func (*ErrIntraLayerAssemblyMismatch) Category() errs.Category { return errs.CategoryContent }

func (*ErrBaselineBlobDigestMismatch) Category() errs.Category { return errs.CategoryContent }
func (*ErrShippedBlobDigestMismatch) Category() errs.Category  { return errs.CategoryContent }

func (*ErrBaselineMissingPatchRef) Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineMissingPatchRef) NextAction() string {
	return "the named baseline lacks the layer this patch was built against"
}

func (*ErrIntraLayerUnsupported) Category() errs.Category { return errs.CategoryUser }
func (*ErrIntraLayerUnsupported) NextAction() string {
	return "retry with --intra-layer=off or provide a baseline with readable blob bytes"
}

func (*ErrPhase1Archive) Category() errs.Category { return errs.CategoryContent }
func (*ErrPhase1Archive) NextAction() string {
	return "re-export the archive with the current diffah"
}

func (*ErrUnknownBundleVersion) Category() errs.Category { return errs.CategoryContent }
func (*ErrUnknownBundleVersion) NextAction() string {
	return "upgrade diffah to a version that supports this archive"
}

func (*ErrInvalidBundleFormat) Category() errs.Category { return errs.CategoryContent }

func (*ErrMultiImageNeedsNamedBaselines) Category() errs.Category { return errs.CategoryUser }
func (*ErrMultiImageNeedsNamedBaselines) NextAction() string {
	return "pass a BASELINE-SPEC JSON file mapping each image name to its baseline path (see 'diffah unbundle --help')"
}

func (*ErrBaselineNameUnknown) Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineNameUnknown) NextAction() string {
	return "check `diffah inspect` for the names this bundle expects"
}

func (*ErrBaselineMismatch) Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineMismatch) NextAction() string {
	return "the supplied baseline has the wrong manifest digest"
}

func (*ErrBaselineMissing) Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineMissing) NextAction() string {
	return "add the missing image(s) to the BASELINE-SPEC JSON, or drop --strict to skip them"
}

func (*ErrInvalidBundleSpec) Category() errs.Category { return errs.CategoryUser }
func (*ErrInvalidBundleSpec) NextAction() string {
	return "check bundle spec JSON syntax and field names"
}

func (*ErrDuplicateBundleName) Category() errs.Category { return errs.CategoryUser }
func (*ErrDuplicateBundleName) NextAction() string {
	return "each image name in a bundle must be unique"
}

func (*ErrRegistryAuth) Category() errs.Category { return errs.CategoryUser }
func (*ErrRegistryAuth) NextAction() string {
	return "verify --authfile or --creds for this registry"
}

func (*ErrRegistryNetwork) Category() errs.Category { return errs.CategoryEnvironment }
func (*ErrRegistryNetwork) NextAction() string {
	return "check connectivity and retry with --retry-times / --retry-delay"
}

func (*ErrRegistryManifestMissing) Category() errs.Category { return errs.CategoryContent }
func (*ErrRegistryManifestMissing) NextAction() string {
	return "manifest was not found — check tag or repository spelling"
}

func (*ErrRegistryManifestInvalid) Category() errs.Category { return errs.CategoryContent }
func (*ErrRegistryManifestInvalid) NextAction() string {
	return "manifest at this reference is corrupt or uses an unsupported schema"
}

var (
	_ errs.Categorized = (*ErrManifestListUnselected)(nil)
	_ errs.Categorized = (*ErrSidecarSchema)(nil)
	_ errs.Categorized = (*ErrBaselineMissingBlob)(nil)
	_ errs.Categorized = (*ErrIncompatibleOutputFormat)(nil)
	_ errs.Categorized = (*ErrSourceManifestUnreadable)(nil)
	_ errs.Categorized = (*ErrDigestMismatch)(nil)
	_ errs.Categorized = (*ErrIntraLayerAssemblyMismatch)(nil)
	_ errs.Categorized = (*ErrBaselineBlobDigestMismatch)(nil)
	_ errs.Categorized = (*ErrShippedBlobDigestMismatch)(nil)
	_ errs.Categorized = (*ErrBaselineMissingPatchRef)(nil)
	_ errs.Categorized = (*ErrIntraLayerUnsupported)(nil)
	_ errs.Categorized = (*ErrPhase1Archive)(nil)
	_ errs.Categorized = (*ErrUnknownBundleVersion)(nil)
	_ errs.Categorized = (*ErrInvalidBundleFormat)(nil)
	_ errs.Categorized = (*ErrMultiImageNeedsNamedBaselines)(nil)
	_ errs.Categorized = (*ErrBaselineNameUnknown)(nil)
	_ errs.Categorized = (*ErrBaselineMismatch)(nil)
	_ errs.Categorized = (*ErrBaselineMissing)(nil)
	_ errs.Categorized = (*ErrInvalidBundleSpec)(nil)
	_ errs.Categorized = (*ErrDuplicateBundleName)(nil)
	_ errs.Categorized = (*ErrRegistryAuth)(nil)
	_ errs.Categorized = (*ErrRegistryNetwork)(nil)
	_ errs.Categorized = (*ErrRegistryManifestMissing)(nil)
	_ errs.Categorized = (*ErrRegistryManifestInvalid)(nil)
)
