package diff

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestErrManifestListUnselected_MentionsRefAndFlag(t *testing.T) {
	err := &ErrManifestListUnselected{Ref: "docker://x/y:v1"}
	require.Contains(t, err.Error(), "docker://x/y:v1")
	require.Contains(t, err.Error(), "--platform")
}

func TestErrUnsupportedSchemaVersion_MentionsBothVersions(t *testing.T) {
	err := &ErrUnsupportedSchemaVersion{Got: "v99"}
	require.Contains(t, err.Error(), "v99")
	require.Contains(t, err.Error(), "v1")
}

func TestErrSidecarSchema_MentionsReason(t *testing.T) {
	err := &ErrSidecarSchema{Reason: "platform required"}
	require.Contains(t, err.Error(), "platform required")
}

func TestErrBaselineMissingBlob_RoundTripsViaErrorsAs(t *testing.T) {
	base := &ErrBaselineMissingBlob{Digest: "sha256:abc", Source: "docker://x"}
	wrapped := fmt.Errorf("outer: %w", base)

	var got *ErrBaselineMissingBlob
	require.True(t, errors.As(wrapped, &got))
	require.Equal(t, base.Digest, got.Digest)
	require.Equal(t, base.Source, got.Source)
}

func TestErrIncompatibleOutputFormat_MentionsBoth(t *testing.T) {
	err := &ErrIncompatibleOutputFormat{SourceMime: "x", OutputFormat: "y"}
	require.Contains(t, err.Error(), "x")
	require.Contains(t, err.Error(), "y")
}

func TestErrSourceManifestUnreadable_UnwrapsCause(t *testing.T) {
	cause := errors.New("network boom")
	err := &ErrSourceManifestUnreadable{Ref: "docker://x", Cause: cause}
	require.ErrorIs(t, err, cause)
}

func TestErrDigestMismatch_MentionsWantGot(t *testing.T) {
	err := &ErrDigestMismatch{Where: "post-export", Want: "sha256:a", Got: "sha256:b"}
	msg := err.Error()
	require.Contains(t, msg, "post-export")
	require.Contains(t, msg, "sha256:a")
	require.Contains(t, msg, "sha256:b")
}
