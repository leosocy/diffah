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

	var env jsonEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, 1, env.SchemaVersion)
	data := env.Data.(map[string]any)
	require.Equal(t, "user", data["category"])
	require.NotEmpty(t, data["message"])
	require.NotEmpty(t, data["next_action"])
}

func TestRenderError_JSON_EnvironmentCategory(t *testing.T) {
	var buf bytes.Buffer
	RenderError(&buf, zstdpatch.ErrZstdBinaryMissing, "json")

	var env jsonEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	data := env.Data.(map[string]any)
	require.Equal(t, "environment", data["category"], "zstd errors should be environment")
	require.NotEmpty(t, data["next_action"], "environment errors should carry an install hint")
}

func TestRenderError_JSON_ContentCategory(t *testing.T) {
	var buf bytes.Buffer
	RenderError(&buf, &diff.ErrDigestMismatch{Where: "blob", Want: "sha256:aa", Got: "sha256:bb"}, "json")

	var env jsonEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	data := env.Data.(map[string]any)
	require.Equal(t, "content", data["category"])
}

func TestRenderError_JSON_InternalCategory(t *testing.T) {
	var buf bytes.Buffer
	RenderError(&buf, errors.New("mystery"), "json")

	var env jsonEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	data := env.Data.(map[string]any)
	require.Equal(t, "internal", data["category"])
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
