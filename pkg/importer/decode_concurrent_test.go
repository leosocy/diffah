package importer

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)

// TestBundleImageSource_ConcurrentSameDigestReadersAreByteIdentical verifies
// that N concurrent GetBlob calls for the same EncodingFull digest each yield
// readers whose drained bytes hash identically. After PR4 each call returns
// an independent path-backed *verifyingReadCloser (its own *os.File), so the
// streams must not interfere.
//
// Gate for PR5's HasThreadSafeGetBlob = true: PR4 keeps the flag at its
// current value (still delegating to s.baseline.HasThreadSafeGetBlob(), which
// returns true for registry baselines today). The EncodingFull contract this
// test pins down is the prerequisite the deliberate-flip-to-true relies on.
//
// EncodingPatch concurrency is bounded by the per-call CreateTemp suffix
// PR4 added in servePatch (compose.go) — distinct goroutines now decode to
// distinct scratch files, so HasThreadSafeGetBlob = true is safe for both
// encodings on the importer side. PR5 should extend this test with an
// EncodingPatch case BEFORE flipping the flag, so the contract is locked
// in code and not just in scratch-path naming.
func TestBundleImageSource_ConcurrentSameDigestReadersAreByteIdentical(t *testing.T) {
	blobDir := t.TempDir()
	payload := []byte("PR5-thread-safe-contract-gate-payload-bytes-here")
	d := digest.FromBytes(payload)
	algoDir := filepath.Join(blobDir, d.Algorithm().String())
	if err := os.MkdirAll(algoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(algoDir, d.Encoded()), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	src := &bundleImageSource{
		blobDir:   blobDir,
		imageName: "svc-thread-safe",
		sidecar: &diff.Sidecar{
			Blobs: map[digest.Digest]diff.BlobEntry{
				d: {Encoding: diff.EncodingFull, Size: int64(len(payload))},
			},
		},
		workdir: t.TempDir(),
	}

	const N = 8
	sums := make([][32]byte, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc, _, err := src.GetBlob(context.Background(), types.BlobInfo{Digest: d}, nil)
			if err != nil {
				errs[i] = err
				return
			}
			data, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				errs[i] = err
				return
			}
			sums[i] = sha256.Sum256(data)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("reader %d failed: %v", i, err)
		}
	}
	for i := 1; i < N; i++ {
		if sums[i] != sums[0] {
			t.Fatalf("reader %d sha != reader 0", i)
		}
	}
	// Sanity: the shared sha must equal the input payload's sha.
	want := sha256.Sum256(payload)
	if sums[0] != want {
		t.Fatalf("digest mismatch: got %x want %x", sums[0], want)
	}
}
