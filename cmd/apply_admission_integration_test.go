//go:build integration

package cmd_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestApply_FailFastWhenImageExceedsBudget proves PR5's pre-pool
// admission check rejects the bundle (CategoryUser → exit 2) before any
// worker starts when --memory-budget is smaller than any single image's
// estimated peak RSS. The error surfaces with the offending budget value
// and a remediation hint mentioning --memory-budget.
//
// Setting --memory-budget=1 (one byte) guarantees rejection: every real
// image's per-image estimate is at least the smallest entry in the
// windowLog→RSS table (256 MiB).
func TestApply_FailFastWhenImageExceedsBudget(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	// Build a real delta so the apply pipeline reaches the admission gate.
	delta := filepath.Join(tmp, "delta.tar")
	{
		cmd := exec.Command(bin,
			"diff",
			"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			"oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
			delta,
		)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "diff: %s", string(out))
	}

	restored := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		"--memory-budget", "1",
		delta,
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"oci-archive:"+restored,
	)

	require.NotEqual(t, 0, exit, "expected non-zero exit when --memory-budget=1; stderr=%q", stderr)
	require.Contains(t, stderr, "memory-budget",
		"expected stderr to mention --memory-budget; stderr=%q", stderr)
	require.Contains(t, stderr, "increase --memory-budget",
		"expected remediation hint to suggest increasing --memory-budget; stderr=%q", stderr)
}

// TestApply_MemoryBudgetZeroDisablesAdmission proves --memory-budget=0
// is the documented opt-out: the admission pre-flight returns nil
// unconditionally (skipping every fits-in-budget check) and the
// admission pool's memSem stays nil. A normal apply must succeed.
func TestApply_MemoryBudgetZeroDisablesAdmission(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	delta := filepath.Join(tmp, "delta.tar")
	{
		cmd := exec.Command(bin,
			"diff",
			"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			"oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
			delta,
		)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "diff: %s", string(out))
	}

	restored := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		"--memory-budget", "0",
		delta,
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"oci-archive:"+restored,
	)

	require.Equal(t, 0, exit, "expected exit 0 with --memory-budget=0 (admission disabled); stderr=%q", stderr)
}
