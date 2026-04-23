package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
)

func TestRenderError_JSON_UserCategory(t *testing.T) {
	var buf bytes.Buffer
	RenderError(&buf, &diff.ErrMultiImageNeedsNamedBaselines{N: 3}, "json")

	var env jsonErrorEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, 1, env.SchemaVersion)
	require.Equal(t, "user", env.Error.Category)
	require.NotEmpty(t, env.Error.Message)
	require.NotEmpty(t, env.Error.NextAction)
}

func TestRenderError_JSON_EnvironmentCategory(t *testing.T) {
	var buf bytes.Buffer
	RenderError(&buf, zstdpatch.ErrZstdBinaryMissing, "json")

	var env jsonErrorEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, "environment", env.Error.Category, "zstd errors should be environment")
	require.NotEmpty(t, env.Error.NextAction, "environment errors should carry an install hint")
}

func TestRenderError_JSON_ContentCategory(t *testing.T) {
	var buf bytes.Buffer
	RenderError(&buf, &diff.ErrDigestMismatch{Where: "blob", Want: "sha256:aa", Got: "sha256:bb"}, "json")

	var env jsonErrorEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, "content", env.Error.Category)
}

func TestRenderError_JSON_InternalCategory(t *testing.T) {
	var buf bytes.Buffer
	RenderError(&buf, errors.New("mystery"), "json")

	var env jsonErrorEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, "internal", env.Error.Category)
}

func TestRenderError_JSON_TextFallback(t *testing.T) {
	var buf bytes.Buffer
	RenderError(&buf, &diff.ErrBaselineMismatch{Name: "x"}, "text")

	out := buf.String()
	require.Contains(t, out, "diffah: user:")
	require.Contains(t, out, "hint:")
}

func TestRenderError_NilError(t *testing.T) {
	var buf bytes.Buffer
	RenderError(&buf, nil, "json")
	require.Empty(t, buf.String(), "nil error should produce no output")
}
