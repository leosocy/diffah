package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestRunBaselinePreflight_AllComplete(t *testing.T) {
	fixture := newBaselinePreflightFixture(t)

	filtered, skipped := runBaselinePreflight(
		context.Background(),
		[]string{testPreflightSvcA, testPreflightSvcB},
		fixture.bundle,
		map[string]resolvedBaseline{
			testPreflightSvcA: {Name: testPreflightSvcA, Src: baselinePreflightSourceWith(fixture.layerA)},
			testPreflightSvcB: {Name: testPreflightSvcB, Src: baselinePreflightSourceWith(fixture.layerB)},
		},
	)

	if !sameStrings(filtered, []string{testPreflightSvcA, testPreflightSvcB}) {
		t.Fatalf("filtered = %v, want both input images", filtered)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want empty", skipped)
	}
}

func TestRunBaselinePreflight_OneImageBaselineMissing(t *testing.T) {
	fixture := newBaselinePreflightFixture(t)

	filtered, skipped := runBaselinePreflight(
		context.Background(),
		[]string{testPreflightSvcA, testPreflightSvcB},
		fixture.bundle,
		map[string]resolvedBaseline{
			testPreflightSvcA: {Name: testPreflightSvcA, Src: baselinePreflightSourceWith(fixture.layerA)},
			testPreflightSvcB: {Name: testPreflightSvcB, Src: baselinePreflightSourceWith()},
		},
	)

	if !sameStrings(filtered, []string{testPreflightSvcA}) {
		t.Fatalf("filtered = %v, want only svc-a", filtered)
	}
	got, ok := skipped[testPreflightSvcB]
	if !ok {
		t.Fatalf("svc-b was not skipped: %v", skipped)
	}
	if got.Status != PreflightBaselineMissing {
		t.Fatalf("svc-b status = %v, want PreflightBaselineMissing", got.Status)
	}
	if got.LayerDigest != fixture.layerB {
		t.Fatalf("svc-b LayerDigest = %s, want %s", got.LayerDigest, fixture.layerB)
	}
	if got.Err != nil {
		t.Fatalf("svc-b Err = %v, want nil for content-missing baseline", got.Err)
	}
}

func TestRunBaselinePreflight_TransportErrorRecordsCause(t *testing.T) {
	fixture := newBaselinePreflightFixture(t)
	transportErr := fmt.Errorf("registry unavailable")

	filtered, skipped := runBaselinePreflight(
		context.Background(),
		[]string{testPreflightSvcA, testPreflightSvcB},
		fixture.bundle,
		map[string]resolvedBaseline{
			testPreflightSvcA: {Name: testPreflightSvcA, Src: baselinePreflightSourceWith(fixture.layerA)},
			testPreflightSvcB: {Name: testPreflightSvcB, Src: &baselinePreflightFakeSource{err: transportErr}},
		},
	)

	if !sameStrings(filtered, []string{testPreflightSvcA}) {
		t.Fatalf("filtered = %v, want only svc-a", filtered)
	}
	got, ok := skipped[testPreflightSvcB]
	if !ok {
		t.Fatalf("svc-b was not skipped: %v", skipped)
	}
	if got.Status != PreflightBaselineMissing {
		t.Fatalf("svc-b status = %v, want PreflightBaselineMissing", got.Status)
	}
	if got.LayerDigest != fixture.layerB {
		t.Fatalf("svc-b LayerDigest = %s, want %s", got.LayerDigest, fixture.layerB)
	}
	if !errors.Is(got.Err, transportErr) {
		t.Fatalf("svc-b Err = %v, want %v", got.Err, transportErr)
	}
}

type baselinePreflightFixture struct {
	bundle       *extractedBundle
	layerA       digest.Digest
	layerB       digest.Digest
	manifestADig digest.Digest
	manifestBDig digest.Digest
}

func newBaselinePreflightFixture(t *testing.T) baselinePreflightFixture {
	t.Helper()

	layerA := digest.FromBytes([]byte("baseline-preflight-layer-a"))
	layerB := digest.FromBytes([]byte("baseline-preflight-layer-b"))
	configA := digest.FromBytes([]byte("baseline-preflight-config-a"))
	configB := digest.FromBytes([]byte("baseline-preflight-config-b"))
	manifestA := synthBaselinePreflightManifest(t, configA, layerA)
	manifestB := synthBaselinePreflightManifest(t, configB, layerB)
	manifestADig := digest.FromBytes(manifestA)
	manifestBDig := digest.FromBytes(manifestB)

	blobDir := t.TempDir()
	writeBaselinePreflightBlob(t, blobDir, manifestADig, manifestA)
	writeBaselinePreflightBlob(t, blobDir, manifestBDig, manifestB)

	sidecar := &diff.Sidecar{
		Blobs: map[digest.Digest]diff.BlobEntry{
			manifestADig: {Encoding: diff.EncodingFull, Size: int64(len(manifestA))},
			manifestBDig: {Encoding: diff.EncodingFull, Size: int64(len(manifestB))},
			configA:      {Encoding: diff.EncodingFull, Size: 10},
			configB:      {Encoding: diff.EncodingFull, Size: 10},
		},
		Images: []diff.ImageEntry{
			{
				Name:   testPreflightSvcA,
				Target: diff.TargetRef{ManifestDigest: manifestADig, MediaType: imgspecv1.MediaTypeImageManifest},
			},
			{
				Name:   testPreflightSvcB,
				Target: diff.TargetRef{ManifestDigest: manifestBDig, MediaType: imgspecv1.MediaTypeImageManifest},
			},
		},
	}

	return baselinePreflightFixture{
		bundle:       &extractedBundle{blobDir: blobDir, sidecar: sidecar},
		layerA:       layerA,
		layerB:       layerB,
		manifestADig: manifestADig,
		manifestBDig: manifestBDig,
	}
}

func synthBaselinePreflightManifest(
	t *testing.T, config digest.Digest, layers ...digest.Digest,
) []byte {
	t.Helper()

	type descriptor struct {
		MediaType string        `json:"mediaType"`
		Digest    digest.Digest `json:"digest"`
		Size      int64         `json:"size"`
	}
	type manifest struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Config        descriptor   `json:"config"`
		Layers        []descriptor `json:"layers"`
	}
	m := manifest{
		SchemaVersion: 2,
		MediaType:     imgspecv1.MediaTypeImageManifest,
		Config: descriptor{
			MediaType: imgspecv1.MediaTypeImageConfig,
			Digest:    config,
			Size:      10,
		},
	}
	for _, layer := range layers {
		m.Layers = append(m.Layers, descriptor{
			MediaType: imgspecv1.MediaTypeImageLayerGzip,
			Digest:    layer,
			Size:      100,
		})
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeBaselinePreflightBlob(t *testing.T, blobDir string, d digest.Digest, raw []byte) {
	t.Helper()

	algoDir := filepath.Join(blobDir, d.Algorithm().String())
	if err := os.MkdirAll(algoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(algoDir, d.Encoded()), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func baselinePreflightSourceWith(digests ...digest.Digest) *baselinePreflightFakeSource {
	available := make(map[digest.Digest]struct{}, len(digests))
	for _, d := range digests {
		available[d] = struct{}{}
	}
	return &baselinePreflightFakeSource{available: available}
}

type baselinePreflightFakeSource struct {
	available map[digest.Digest]struct{}
	err       error
}

func (*baselinePreflightFakeSource) Reference() types.ImageReference { return nil }
func (*baselinePreflightFakeSource) Close() error                    { return nil }
func (*baselinePreflightFakeSource) GetManifest(context.Context, *digest.Digest) ([]byte, string, error) {
	return nil, "", nil
}
func (*baselinePreflightFakeSource) HasThreadSafeGetBlob() bool { return true }
func (*baselinePreflightFakeSource) GetSignatures(context.Context, *digest.Digest) ([][]byte, error) {
	return nil, nil
}
func (*baselinePreflightFakeSource) LayerInfosForCopy(context.Context, *digest.Digest) ([]types.BlobInfo, error) {
	return nil, nil
}
func (s *baselinePreflightFakeSource) GetBlob(
	_ context.Context, info types.BlobInfo, _ types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	if s.err != nil {
		return nil, 0, s.err
	}
	if _, ok := s.available[info.Digest]; !ok {
		return nil, 0, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader([]byte("x"))), 1, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
