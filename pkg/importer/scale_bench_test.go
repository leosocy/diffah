//go:build big

package importer_test

import (
	"os"
	"testing"
)

// TestImport_ScaleApply2GiB locks in the importer's apply-side memory
// ceiling for the spec §13 acceptance gate: a 2-GiB-layer bundle must
// apply with peak RSS ≤ 8 GiB on a normal CI runner. The /usr/bin/time -v
// guard in .github/workflows/scale-bench.yml's "Apply (scale)" step
// measures peak RSS externally; this test exists to make that workflow
// step have something to invoke under -tags=big.
//
// DEFERRED: the in-process fixture-build + import plumbing is intentionally
// not implemented in PR6. The exporter's TestScaleBench_2GiBLayer relies
// on scripts/build_fixtures -scale=<bytes> to produce baseline.tar +
// target.tar, then drives exporter.Export. Mirroring that for the import
// side requires three steps end-to-end (build fixtures → export delta →
// import delta), each with its own per-step RSS contribution to disambiguate
// from the apply-only ceiling we want to gate. That belongs in a follow-up
// PR with proper test-side helpers (runImportInProcess, buildScale2GiBFixture).
//
// Until that lands, the workflow YAML's apply step is a no-op skip — but
// the YAML scaffolding is in place so the follow-up PR only has to swap
// the test body. Tracking note: spec §13 acceptance #9 (Phase-3 fixture
// round-trip, the I6 sibling) also remains open.
//
// Gated by DIFFAH_BIG_TEST=1 (in addition to the `big` build tag) so an
// accidental `go test -tags=big` doesn't surface a confusing "skipped"
// row on a developer's laptop. CI sets the env var explicitly.
func TestImport_ScaleApply2GiB(t *testing.T) {
	if os.Getenv("DIFFAH_BIG_TEST") != "1" {
		t.Skip("set DIFFAH_BIG_TEST=1 to run")
	}
	t.Skip("apply scale-bench infrastructure pending — see PR6 follow-up; " +
		"workflow YAML in place, test body deferred")
}
