//go:build integration

package cmd_test

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExit_UserError_MissingRequiredFlag(t *testing.T) {
	bin := integrationBinary(t)
	_, stderr, exit := runDiffahBin(t, bin, "export")
	require.Equal(t, 2, exit, "expected exit 2 (user) for missing required flag; stderr=%q", stderr)
	require.True(t, strings.Contains(stderr, "user:") || strings.Contains(stderr, "required"),
		"expected user-category or 'required' in stderr; got %q", stderr)
}

func TestExit_UserError_UnknownSubcommand(t *testing.T) {
	bin := integrationBinary(t)
	_, stderr, exit := runDiffahBin(t, bin, "no-such-subcommand")
	require.Equal(t, 2, exit, "expected exit 2 (user) for unknown subcommand; stderr=%q", stderr)
	require.Contains(t, stderr, "diffah:")
}

func TestExit_EnvError_MissingFile(t *testing.T) {
	bin := integrationBinary(t)
	_, _, exit := runDiffahBin(t, bin, "inspect", filepath.Join(os.TempDir(), "nonexistent_diffah_test.tar"))
	require.Equal(t, 3, exit, "expected exit 3 (environment) for missing file")
}

func TestExit_ContentError_UnknownBundleVersion(t *testing.T) {
	bin := integrationBinary(t)
	fixture := forgeV999Archive(t)
	_, stderr, exit := runDiffahBin(t, bin, "inspect", fixture)
	require.Equal(t, 4, exit, "expected exit 4 (content) for unknown bundle version; stderr=%q", stderr)
	require.Contains(t, stderr, "content:", "expected content-category error")
}

func forgeV999Archive(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "forged_v999.tar")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	tw := tar.NewWriter(f)
	sidecar := `{"version":"v999","feature":"bundle","tool":"test","tool_version":"0.0.1","platform":"linux/amd64","images":[],"blobs":[]}`
	hdr := &tar.Header{
		Name: "diffah.json",
		Mode: 0o644,
		Size: int64(len(sidecar)),
	}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err = tw.Write([]byte(sidecar))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	return path
}
