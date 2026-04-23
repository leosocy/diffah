package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

func TestDoctorErr_CategoryAndHint(t *testing.T) {
	var err doctorErr
	require.Equal(t, errs.CategoryEnvironment, err.Category())
	require.Equal(t, "see failing check for its specific hint", err.NextAction())
	require.Equal(t, "one or more checks failed", err.Error())
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

func TestDefaultChecks_ContainsZstd(t *testing.T) {
	checks := defaultChecks()
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name()
	}
	require.Contains(t, names, "zstd")
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

type stubCheck struct {
	name string
}

func (s stubCheck) Name() string                      { return s.name }
func (s stubCheck) Run(_ context.Context) CheckResult { return CheckResult{} }
