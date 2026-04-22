package diff

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
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
