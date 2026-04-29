package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

func TestDoctorChecksFailed_ClassifiesAsEnvironment(t *testing.T) {
	cat, hint := errs.Classify(errDoctorChecksFailed)
	require.Equal(t, errs.CategoryEnvironment, cat)
	require.Equal(t, "see failing check for its specific hint", hint)
	require.Equal(t, "one or more checks failed", errDoctorChecksFailed.Error())
}

func TestStatusLabel(t *testing.T) {
	tests := []struct {
		status, detail, want string
	}{
		{"ok", "", "ok"},
		{"ok", "1.5.5 via /usr/bin/zstd", "ok (1.5.5 via /usr/bin/zstd)"},
		{"warn", "", "warn"},
		{"warn", "slow disk", "warn (slow disk)"},
		{"fail", "", "fail"},
		{"fail", "zstd not on $PATH", "fail (zstd not on $PATH)"},
	}
	for _, tc := range tests {
		got := statusLabel(tc.status, tc.detail)
		require.Equal(t, tc.want, got, "statusLabel(%q, %q)", tc.status, tc.detail)
	}
}

func TestRenderDoctorText(t *testing.T) {
	checks := []Check{
		stubCheck{name: "zstd"},
		stubCheck{name: "network"},
	}
	results := []CheckResult{
		{Status: statusOK, Detail: "available"},
		{Status: statusFail, Detail: "unreachable", Hint: "check your proxy"},
	}

	var buf bytes.Buffer
	renderDoctorText(&buf, checks, results)

	out := buf.String()
	require.Contains(t, out, "zstd")
	require.Contains(t, out, "ok (available)")
	require.Contains(t, out, "network")
	require.Contains(t, out, "fail (unreachable)")
	require.Contains(t, out, "hint: check your proxy")
}

func TestAnyFailed(t *testing.T) {
	require.False(t, anyFailed([]CheckResult{{Status: statusOK}}))
	require.False(t, anyFailed([]CheckResult{{Status: statusOK}, {Status: statusWarn}}))
	require.True(t, anyFailed([]CheckResult{{Status: statusOK}, {Status: statusFail}}))
	require.True(t, anyFailed([]CheckResult{{Status: statusFail}}))
}

func TestDefaultChecks_ReturnsFiveChecksInOrder(t *testing.T) {
	checks := defaultChecks("", nil)
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name()
	}
	require.Equal(t, []string{"zstd", "tmpdir", "authfile", "network", "config"}, names)
}

func TestZstdCheck_NameAndRun(t *testing.T) {
	var c zstdCheck
	require.Equal(t, "zstd", c.Name())
	result := c.Run(context.Background())
	require.Contains(t, []string{statusOK, statusFail}, result.Status)
	if result.Status == statusOK {
		require.NotEmpty(t, result.Detail)
		require.Empty(t, result.Hint)
	} else {
		require.NotEmpty(t, result.Detail)
		require.NotEmpty(t, result.Hint)
	}
}

func TestTmpdirCheck_NameAndOK(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	c := tmpdirCheck{}
	require.Equal(t, "tmpdir", c.Name())
	result := c.Run(context.Background())
	require.Equal(t, statusOK, result.Status)
	require.NotEmpty(t, result.Detail)
	require.Empty(t, result.Hint)
}

func TestTmpdirCheck_FailWhenDirDoesNotExist(t *testing.T) {
	t.Setenv("TMPDIR", "/nonexistent/diffah-doctor-test/path")
	result := tmpdirCheck{}.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.NotEmpty(t, result.Detail)
	require.NotEmpty(t, result.Hint)
}

func TestAuthfileCheck_WarnWhenChainEmpty(t *testing.T) {
	// Empty all three env vars so ResolveAuthFile returns "".
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir()) // empty home dir — no .docker/config.json

	result := authfileCheck{}.Run(context.Background())
	require.Equal(t, statusWarn, result.Status)
	require.Contains(t, result.Detail, "anonymous pulls only")
}

func TestAuthfileCheck_OKForValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"auths": {
			"registry.example.com": {"auth": "abc"},
			"docker.io": {"auth": "def"}
		}
	}`), 0o600))
	t.Setenv("REGISTRY_AUTH_FILE", path)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir())

	result := authfileCheck{}.Run(context.Background())
	require.Equal(t, statusOK, result.Status)
	require.Contains(t, result.Detail, path)
	require.Contains(t, result.Detail, "2 registries")
}

func TestAuthfileCheck_FailOnMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(path, []byte("{this is not json"), 0o600))
	t.Setenv("REGISTRY_AUTH_FILE", path)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir())

	result := authfileCheck{}.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.Contains(t, result.Detail, "JSON parse error")
	require.NotEmpty(t, result.Hint)
}

func TestAuthfileCheck_FailWhenAuthsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"other": {}}`), 0o600))
	t.Setenv("REGISTRY_AUTH_FILE", path)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir())

	result := authfileCheck{}.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.Contains(t, result.Detail, "missing 'auths' map")
}

func TestNetworkCheck_SkippedWhenProbeEmpty(t *testing.T) {
	c := networkCheck{probe: "", buildSysCtx: nil}
	result := c.Run(context.Background())
	require.Equal(t, statusOK, result.Status)
	require.Contains(t, result.Detail, "skipped")
}

func TestNetworkCheck_FailWhenBuildSysCtxFails(t *testing.T) {
	stub := func() (*types.SystemContext, int, time.Duration, error) {
		return nil, 0, 0, errors.New("flag conflict: --creds and --no-creds")
	}
	c := networkCheck{probe: "docker://example.com/foo:tag", buildSysCtx: stub}
	result := c.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.Contains(t, result.Detail, "flag conflict")
	require.NotEmpty(t, result.Hint)
}

func TestNetworkCheck_FailOnInvalidReference(t *testing.T) {
	stub := func() (*types.SystemContext, int, time.Duration, error) {
		return &types.SystemContext{}, 0, 0, nil
	}
	c := networkCheck{probe: "not a valid reference", buildSysCtx: stub}
	result := c.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.Contains(t, result.Detail, "parse")
}

func TestConfigCheck_OKWhenFileAbsent(t *testing.T) {
	t.Setenv("DIFFAH_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	result := configCheck{}.Run(context.Background())
	require.Equal(t, statusOK, result.Status)
	require.Contains(t, result.Detail, "no config file")
}

func TestConfigCheck_OKForValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("platform: linux/arm64\n"), 0o600))
	t.Setenv("DIFFAH_CONFIG", path)

	result := configCheck{}.Run(context.Background())
	require.Equal(t, statusOK, result.Status)
	require.Contains(t, result.Detail, path)
}

func TestConfigCheck_FailForMalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("not: valid: yaml: ["), 0o600))
	t.Setenv("DIFFAH_CONFIG", path)

	result := configCheck{}.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.Contains(t, result.Detail, "yaml")
	require.NotEmpty(t, result.Hint)
}

type stubCheck struct {
	name string
}

func (s stubCheck) Name() string                      { return s.name }
func (s stubCheck) Run(_ context.Context) CheckResult { return CheckResult{} }
