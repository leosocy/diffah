package exporter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestBlobPool_AddEntryIfAbsent_WritesSpillFile(t *testing.T) {
	dir := t.TempDir()
	pool := newBlobPool(dir)
	d := digest.FromBytes([]byte("hello"))
	if err := pool.addEntryIfAbsent(d, []byte("hello"),
		diff.BlobEntry{Size: 5, ArchiveSize: 5, Encoding: diff.EncodingFull}); err != nil {
		t.Fatalf("addEntryIfAbsent: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, d.Encoded()))
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("spill content mismatch: got %q", got)
	}
}

func TestBlobPool_AddEntryIfAbsent_FirstWriteWins(t *testing.T) {
	dir := t.TempDir()
	pool := newBlobPool(dir)
	d := digest.FromBytes([]byte("a"))
	if err := pool.addEntryIfAbsent(d, []byte("first"), diff.BlobEntry{Size: 5, ArchiveSize: 5}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := pool.addEntryIfAbsent(d, []byte("second"), diff.BlobEntry{Size: 6, ArchiveSize: 6}); err != nil {
		t.Fatalf("second add: %v", err)
	}
	if got := pool.entries[d].Size; got != 5 {
		t.Fatalf("first-write-wins violated: entry size=%d, want 5", got)
	}
}

func TestBlobPool_AddEntryIfAbsent_ConcurrentWritersForSameDigestSucceed(t *testing.T) {
	dir := t.TempDir()
	pool := newBlobPool(dir)
	payload := []byte("concurrent")
	d := digest.FromBytes(payload)

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := pool.addEntryIfAbsent(d, payload,
				diff.BlobEntry{Size: int64(len(payload)), ArchiveSize: int64(len(payload))}); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent add: %v", err)
	}
	if len(pool.spills) != 1 {
		t.Fatalf("expected 1 spill entry after concurrent writes, got %d", len(pool.spills))
	}
	got, err := os.ReadFile(filepath.Join(dir, d.Encoded()))
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("spill content corrupted by concurrent writes: got %q want %q", got, payload)
	}
}

func TestBlobPool_SortedDigestsIsLex(t *testing.T) {
	dir := t.TempDir()
	pool := newBlobPool(dir)
	for _, payload := range [][]byte{[]byte("c"), []byte("a"), []byte("b")} {
		d := digest.FromBytes(payload)
		if err := pool.addEntryIfAbsent(d, payload, diff.BlobEntry{Size: 1, ArchiveSize: 1}); err != nil {
			t.Fatalf("addEntryIfAbsent: %v", err)
		}
	}
	got := pool.sortedDigests()
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("not sorted: %v", got)
		}
	}
}

// TestBlobPool_ShipRefCount ports the refCount/countShipped coverage from
// the old TestBlobPool_AddIfAbsentAndRefCount test.
func TestBlobPool_ShipRefCount(t *testing.T) {
	dir := t.TempDir()
	pool := newBlobPool(dir)
	d := digest.Digest("sha256:aa")
	require.NoError(t, pool.addEntryIfAbsent(d, []byte("hi"), diff.BlobEntry{Size: 2, Encoding: diff.EncodingFull, ArchiveSize: 2}))
	// second write for same digest must be a no-op (first-write-wins)
	require.NoError(t, pool.addEntryIfAbsent(d, []byte("REPLACED"), diff.BlobEntry{Size: 8, Encoding: diff.EncodingFull, ArchiveSize: 8}))
	require.True(t, pool.has(d), "digest must be present")
	require.Equal(t, int64(2), pool.entries[d].Size, "first write wins: size must be 2")

	pool.countShipped(d)
	pool.countShipped(d)
	require.Equal(t, 2, pool.refCount(d))
}

// TestBlobPool_ConcurrentAddDifferentDigests verifies that concurrent
// writes for N distinct digests all land with N distinct spill entries.
func TestBlobPool_ConcurrentAddDifferentDigests(t *testing.T) {
	dir := t.TempDir()
	pool := newBlobPool(dir)
	const N = 64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := []byte(fmt.Sprintf("blob-%d", i))
			d := digest.FromBytes(payload)
			require.NoError(t, pool.addEntryIfAbsent(d, payload, diff.BlobEntry{Size: int64(len(payload))}))
		}()
	}
	wg.Wait()
	if got := len(pool.sortedDigests()); got != N {
		t.Fatalf("digests = %d, want %d", got, N)
	}
	// Verify each digest's on-disk spill contains the expected payload.
	for i := 0; i < N; i++ {
		expected := []byte(fmt.Sprintf("blob-%d", i))
		d := digest.FromBytes(expected)
		got, err := os.ReadFile(filepath.Join(dir, d.Encoded()))
		if err != nil {
			t.Fatalf("read spill for blob-%d: %v", i, err)
		}
		if !bytes.Equal(got, expected) {
			t.Fatalf("spill content mismatch for blob-%d: got %q want %q", i, got, expected)
		}
	}
}

func TestBlobPool_SeedManifestAndConfig(t *testing.T) {
	ctx := context.Background()
	p1, err := planPair(ctx, Pair{Name: "a", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar"}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)
	p2, err := planPair(ctx, Pair{Name: "b", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar"}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)

	dir := t.TempDir()
	pool := newBlobPool(dir)
	require.NoError(t, seedManifestAndConfig(pool, p1))
	require.NoError(t, seedManifestAndConfig(pool, p2))

	mfDigest := digest.FromBytes(p1.TargetManifest)
	require.True(t, pool.has(mfDigest))
	require.True(t, pool.has(p1.TargetConfigDesc.Digest))
	require.Len(t, pool.sortedDigests(), 2, "same target → dedup to 2 unique blobs")
}

func TestEncodeShipped_ForcesFullOnCrossImageDup(t *testing.T) {
	ctx := context.Background()
	p1, err := planPair(ctx, Pair{Name: "a",
		BaselineRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
		TargetRef:   "oci-archive:../../testdata/fixtures/v3_oci.tar"}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)
	p2, err := planPair(ctx, Pair{Name: "b",
		BaselineRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
		TargetRef:   "oci-archive:../../testdata/fixtures/v3_oci.tar"}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)

	dir := t.TempDir()
	pool := newBlobPool(dir)
	require.NoError(t, seedManifestAndConfig(pool, p1))
	require.NoError(t, seedManifestAndConfig(pool, p2))
	for _, p := range []*pairPlan{p1, p2} {
		for _, s := range p.Shipped {
			pool.countShipped(s.Digest)
		}
	}
	require.NoError(t, encodeShipped(ctx, pool, []*pairPlan{p1, p2}, "off", nil, nil, 0, 0, 0, 0, t.TempDir()))

	for _, s := range p1.Shipped {
		entry := pool.entries[s.Digest]
		require.Equal(t, diff.EncodingFull, entry.Encoding, "shared shipped must be full")
	}
}

// TestBlobPool_AddEntryFromPath_RenamesAndRecords verifies the happy path:
// an existing file at srcPath is renamed into the pool's canonical path,
// the spills map is updated, and the file is readable from the canonical path.
func TestBlobPool_AddEntryFromPath_RenamesAndRecords(t *testing.T) {
	poolDir := t.TempDir()
	pool := newBlobPool(poolDir)

	srcDir := t.TempDir()
	payload := []byte("hello-from-path")
	d := digest.FromBytes(payload)
	srcPath := filepath.Join(srcDir, "candidate-file")
	require.NoError(t, os.WriteFile(srcPath, payload, 0o600))

	e := diff.BlobEntry{Size: int64(len(payload)), ArchiveSize: int64(len(payload)), Encoding: diff.EncodingFull}
	require.NoError(t, pool.addEntryFromPath(d, srcPath, e))

	// srcPath must no longer exist (it was renamed).
	_, err := os.Stat(srcPath)
	require.True(t, os.IsNotExist(err), "srcPath should have been moved")

	// Canonical path must exist and contain the original bytes.
	canonPath := filepath.Join(poolDir, d.Encoded())
	got, err := os.ReadFile(canonPath)
	require.NoError(t, err)
	require.Equal(t, payload, got)

	// Pool metadata must reflect the entry.
	require.True(t, pool.has(d))
	require.Equal(t, int64(len(payload)), pool.entries[d].Size)
}

// TestBlobPool_AddEntryFromPath_ConcurrentSameDigest_FirstWriteWins verifies
// that N concurrent goroutines each calling addEntryFromPath with the same
// digest and byte-identical content produce exactly one pool entry, exactly one
// canonical file, and no leftover srcPaths. This exercises the post-rename
// lost-race branch (pool.go:100-106) which sequential tests cannot reach
// because the early RLock short-circuit (pool.go:88-95) fires first.
func TestBlobPool_AddEntryFromPath_ConcurrentSameDigest_FirstWriteWins(t *testing.T) {
	poolDir := t.TempDir()
	pool := newBlobPool(poolDir)
	payload := []byte("byte-identical")
	d := digest.FromBytes(payload)

	const N = 16
	srcDir := t.TempDir()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		i := i
		src := filepath.Join(srcDir, fmt.Sprintf("src-%d", i))
		require.NoError(t, os.WriteFile(src, payload, 0o600))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			require.NoError(t, pool.addEntryFromPath(d, src,
				diff.BlobEntry{Size: int64(len(payload))}))
		}()
	}
	close(start)
	wg.Wait()

	require.Len(t, pool.spills, 1)
	canon, err := os.ReadFile(filepath.Join(poolDir, d.Encoded()))
	require.NoError(t, err)
	require.Equal(t, payload, canon)
	// Every losing srcPath must be removed.
	entries, _ := os.ReadDir(srcDir)
	require.Empty(t, entries, "all losing srcPaths must be cleaned up")
}

// TestBlobPool_AddEntryFromPath_FirstWriteWinsOnCollision verifies that a
// second call for the same digest removes its srcPath without overwriting
// the canonical entry in the pool (first-write-wins, content-addressed).
func TestBlobPool_AddEntryFromPath_FirstWriteWinsOnCollision(t *testing.T) {
	poolDir := t.TempDir()
	pool := newBlobPool(poolDir)

	srcDir := t.TempDir()
	payload := []byte("byte-identical-content")
	d := digest.FromBytes(payload)

	// First write succeeds.
	first := filepath.Join(srcDir, "first")
	require.NoError(t, os.WriteFile(first, payload, 0o600))
	e1 := diff.BlobEntry{Size: int64(len(payload)), ArchiveSize: int64(len(payload)), Encoding: diff.EncodingFull}
	require.NoError(t, pool.addEntryFromPath(d, first, e1))

	// Second write: provide a second source file with byte-identical content
	// but a different entry (to confirm first-write-wins).
	second := filepath.Join(srcDir, "second")
	require.NoError(t, os.WriteFile(second, payload, 0o600))
	e2 := diff.BlobEntry{Size: 999, ArchiveSize: 999, Encoding: diff.EncodingFull}
	require.NoError(t, pool.addEntryFromPath(d, second, e2))

	// srcPath for the second call should have been removed (lost race).
	_, err := os.Stat(second)
	require.True(t, os.IsNotExist(err), "second srcPath should have been removed")

	// First entry must be preserved (size=original, not 999).
	require.Equal(t, int64(len(payload)), pool.entries[d].Size,
		"first-write-wins: entry must reflect the first write")
}
