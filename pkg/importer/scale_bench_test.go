//go:build big

package importer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/diff/errs"
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

// TestImport_ScaleBaselineOnlyReuse4GiB is the hardening-PR2 scale gate for
// the admission contract: baseline-only layers must count against
// --memory-budget even though they are absent from sidecar.Blobs.
//
// The full apply RSS measurement is performed by the surrounding CI job using
// /usr/bin/time -v. This in-process guard exercises the fail-fast admission
// path with a synthetic 4 GiB baseline-only layer so the nightly job has a
// concrete PR2 regression test to invoke.
func TestImport_ScaleBaselineOnlyReuse4GiB(t *testing.T) {
	if os.Getenv("DIFFAH_SCALE_BENCH") != "1" {
		t.Skip("set DIFFAH_SCALE_BENCH=1 to run")
	}

	blobDir := filepath.Join(t.TempDir(), "blobs")
	layerDigest := digest.FromBytes([]byte("baseline-only-4g-layer"))
	manifestDigest := writeScaleManifest(t, blobDir, layerDigest, 4<<30)
	images := []diff.ImageEntry{
		{Name: "svc-a", Target: diff.TargetRef{ManifestDigest: manifestDigest}},
		{Name: "svc-b", Target: diff.TargetRef{ManifestDigest: manifestDigest}},
	}

	err := checkSingleImageFitsInBudget(images, blobDir, map[digest.Digest]diff.BlobEntry{}, 0, 2<<30)
	var userErr *errs.UserError
	if !errors.As(err, &userErr) || userErr.Cat != errs.CategoryUser {
		t.Fatalf("expected CategoryUser budget rejection, got %T: %v", err, err)
	}
	if err := checkSingleImageFitsInBudget(images, blobDir, map[digest.Digest]diff.BlobEntry{}, 0, 8<<30); err != nil {
		t.Fatalf("expected 8 GiB budget to admit baseline-only reuse fixture: %v", err)
	}
}

func writeScaleManifest(t *testing.T, blobDir string, layerDigest digest.Digest, size int64) digest.Digest {
	t.Helper()

	raw, err := json.Marshal(struct {
		SchemaVersion int `json:"schemaVersion"`
		Layers        []struct {
			Digest digest.Digest `json:"digest"`
			Size   int64         `json:"size"`
		} `json:"layers"`
	}{
		SchemaVersion: 2,
		Layers: []struct {
			Digest digest.Digest `json:"digest"`
			Size   int64         `json:"size"`
		}{{Digest: layerDigest, Size: size}},
	})
	if err != nil {
		t.Fatalf("marshal scale manifest: %v", err)
	}
	d := digest.FromBytes(raw)
	dir := filepath.Join(blobDir, d.Algorithm().String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, d.Encoded()), raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return d
}
