//go:build integration

package cmd_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnbundleCLI_PerImageBaselineCompletenessPreflight(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)

	t.Run("partial mode skips only the image with an incomplete baseline", func(t *testing.T) {
		tmp := t.TempDir()
		bundlePath, svcABaseline, svcBStrippedBaseline :=
			buildTwoImageBundleWithB2(t, bin, root, tmp)

		baselinesPath, outputsPath, svcAOut, svcBOut := writeBaselineCompletenessSpecs(
			t, tmp, svcABaseline, svcBStrippedBaseline,
		)

		_, stderr, exit := runDiffahBin(t, bin, "unbundle",
			bundlePath, baselinesPath, outputsPath)

		require.Equalf(t, 0, exit, "partial mode exit = %d, stderr=%s", exit, stderr)
		require.Contains(t, stderr, "applied 1/2")
		require.Contains(t, stderr, "skip svc-b: preflight skipped")
		require.Contains(t, stderr, "not shipped in delta")
		require.FileExists(t, svcAOut)
		if _, err := os.Stat(svcBOut); err == nil {
			t.Fatalf("svc-b output should not exist after preflight skip")
		}
	})

	t.Run("strict mode aborts before applying any image", func(t *testing.T) {
		tmp := t.TempDir()
		bundlePath, svcABaseline, svcBStrippedBaseline :=
			buildTwoImageBundleWithB2(t, bin, root, tmp)

		baselinesPath, outputsPath, svcAOut, svcBOut := writeBaselineCompletenessSpecs(
			t, tmp, svcABaseline, svcBStrippedBaseline,
		)

		_, stderr, exit := runDiffahBin(t, bin, "unbundle", "--strict",
			bundlePath, baselinesPath, outputsPath)

		require.Equalf(t, 4, exit, "strict mode exit = %d, stderr=%s", exit, stderr)
		require.Contains(t, stderr, "skip svc-b: preflight skipped")
		if _, err := os.Stat(svcAOut); err == nil {
			t.Fatalf("svc-a output should not exist after strict preflight abort")
		}
		if _, err := os.Stat(svcBOut); err == nil {
			t.Fatalf("svc-b output should not exist after strict preflight abort")
		}
	})
}

func writeBaselineCompletenessSpecs(
	t *testing.T, tmp, svcABaseline, svcBStrippedBaseline string,
) (baselinesPath, outputsPath, svcAOut, svcBOut string) {
	t.Helper()

	baselinesPath = filepath.Join(tmp, "baselines.json")
	writeJSONFile(t, baselinesPath, map[string]any{
		"baselines": map[string]string{
			"svc-a": "oci-archive:" + svcABaseline,
			"svc-b": "oci-archive:" + svcBStrippedBaseline,
		},
	})

	svcAOut = filepath.Join(tmp, "svc-a.tar")
	svcBOut = filepath.Join(tmp, "svc-b.tar")
	outputsPath = filepath.Join(tmp, "outputs.json")
	writeJSONFile(t, outputsPath, map[string]any{
		"outputs": map[string]string{
			"svc-a": "oci-archive:" + svcAOut,
			"svc-b": "oci-archive:" + svcBOut,
		},
	})
	return baselinesPath, outputsPath, svcAOut, svcBOut
}
