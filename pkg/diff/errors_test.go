package diff

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

func TestBundleErrorMessages(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"phase1", &ErrPhase1Archive{}, "Phase 1 schema"},
		{"unknown version", &ErrUnknownBundleVersion{Got: "v9"}, `unknown bundle version "v9"`},
		{"invalid format", &ErrInvalidBundleFormat{Cause: errors.New("x")}, "invalid bundle format"},
		{"multi image needs named", &ErrMultiImageNeedsNamedBaselines{}, "multi-image"},
		{"baseline unknown", &ErrBaselineNameUnknown{Name: "foo", Available: []string{"a", "b"}}, "not in bundle"},
		{"baseline mismatch", &ErrBaselineMismatch{Name: "a", Expected: "sha256:xx", Got: "sha256:yy"}, "mismatch"},
		{"baseline missing", &ErrBaselineMissing{Names: []string{"b"}}, "missing"},
		{"invalid spec", &ErrInvalidBundleSpec{Reason: "bad"}, "bundle spec"},
		{"duplicate name", &ErrDuplicateBundleName{Name: "a"}, "duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Contains(t, tc.err.Error(), tc.want)
		})
	}
}

func TestErrBaselineBlobDigestMismatch_Message(t *testing.T) {
	e := &ErrBaselineBlobDigestMismatch{
		ImageName: "svc-a",
		Digest:    "sha256:aa",
		Got:       "sha256:bb",
	}
	require.Contains(t, e.Error(), "svc-a")
	require.Contains(t, e.Error(), "sha256:aa")
	require.Contains(t, e.Error(), "sha256:bb")
}

func TestErrShippedBlobDigestMismatch_Message(t *testing.T) {
	e := &ErrShippedBlobDigestMismatch{
		ImageName: "svc-a",
		Digest:    "sha256:aa",
		Got:       "sha256:bb",
	}
	require.Contains(t, e.Error(), "svc-a")
	require.Contains(t, e.Error(), "shipped blob")
	require.Contains(t, e.Error(), "sha256:aa")
	require.Contains(t, e.Error(), "sha256:bb")
}

func TestErrRegistryAuth_Classify(t *testing.T) {
	err := &ErrRegistryAuth{Registry: "ghcr.io"}
	cat, hint := errs.Classify(err)
	require.Equal(t, errs.CategoryUser, cat)
	require.Equal(t, "verify --authfile or --creds for this registry", hint)
	require.Contains(t, err.Error(), "ghcr.io")
	require.Contains(t, err.Error(), "authentication")
}

func TestErrRegistryNetwork_Classify(t *testing.T) {
	err := &ErrRegistryNetwork{Op: "GET manifest", Cause: errors.New("connection refused")}
	cat, hint := errs.Classify(err)
	require.Equal(t, errs.CategoryEnvironment, cat)
	require.Contains(t, hint, "retry")
	require.Contains(t, err.Error(), "GET manifest")
	require.Contains(t, err.Error(), "connection refused")
}

func TestErrRegistryManifestMissing_Classify(t *testing.T) {
	err := &ErrRegistryManifestMissing{Ref: "docker://ghcr.io/org/app:v1"}
	cat, hint := errs.Classify(err)
	require.Equal(t, errs.CategoryContent, cat)
	require.Contains(t, hint, "tag or repository")
	require.Contains(t, err.Error(), "docker://ghcr.io/org/app:v1")
}

func TestErrRegistryManifestInvalid_Classify(t *testing.T) {
	err := &ErrRegistryManifestInvalid{Ref: "docker://x/y:z", Reason: "unsupported schema"}
	cat, hint := errs.Classify(err)
	require.Equal(t, errs.CategoryContent, cat)
	require.Contains(t, hint, "corrupt or uses an unsupported schema")
	require.Contains(t, err.Error(), "unsupported schema")
}

func TestEveryErrorType_IsCategorized(t *testing.T) {
	instances := []any{
		&ErrManifestListUnselected{},
		&ErrSidecarSchema{},
		&ErrBaselineMissingBlob{},
		&ErrIncompatibleOutputFormat{},
		&ErrSourceManifestUnreadable{},
		&ErrDigestMismatch{},
		&ErrIntraLayerAssemblyMismatch{},
		&ErrBaselineBlobDigestMismatch{},
		&ErrShippedBlobDigestMismatch{},
		&ErrBaselineMissingPatchRef{},
		&ErrIntraLayerUnsupported{},
		&ErrPhase1Archive{},
		&ErrUnknownBundleVersion{},
		&ErrInvalidBundleFormat{},
		&ErrMultiImageNeedsNamedBaselines{},
		&ErrBaselineNameUnknown{},
		&ErrBaselineMismatch{},
		&ErrBaselineMissing{},
		&ErrInvalidBundleSpec{},
		&ErrDuplicateBundleName{},
		&ErrRegistryAuth{},
		&ErrRegistryNetwork{},
		&ErrRegistryManifestMissing{},
		&ErrRegistryManifestInvalid{},
	}
	for _, v := range instances {
		cz, ok := v.(errs.Categorized)
		if !ok {
			t.Errorf("%T does not implement errs.Categorized", v)
			continue
		}
		if cz.Category() == errs.CategoryInternal {
			t.Errorf("%T has Category=internal (must be user/env/content)", v)
		}
	}
}
