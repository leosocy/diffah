package exporter

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// makeGzipTarBlob builds a realistic gzip-of-tar fixture:
// tar.Reader stops at the TAR-EOF marker leaving the gzip trailer
// unread — exactly the path that exposes the drain-on-success bug.
func makeGzipTarBlob(t *testing.T, data []byte) ([]byte, digest.Digest) {
	t.Helper()
	tarBlob := buildTarBlob(t, tarEntry{name: "a", data: data, typeflag: tar.TypeReg})
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	_, err := gw.Write(tarBlob)
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	b := gz.Bytes()
	return b, digest.FromBytes(b)
}

// makeMeta creates a BaselineLayerMeta for a gzip+tar layer.
func makeMeta(d digest.Digest, size int64) BaselineLayerMeta {
	return BaselineLayerMeta{
		Digest:    d,
		Size:      size,
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
	}
}

func TestBaselineSpool_TeeWritesAndFingerprints(t *testing.T) {
	dir := t.TempDir()
	s := newBaselineSpool(dir)

	gzBytes, d := makeGzipTarBlob(t, []byte("hello spool"))
	meta := makeMeta(d, int64(len(gzBytes)))

	var fetchCalls atomic.Int64
	fetch := func(_ digest.Digest) (io.ReadCloser, error) {
		fetchCalls.Add(1)
		return io.NopCloser(bytes.NewReader(gzBytes)), nil
	}

	entry, err := s.GetOrSpool(context.Background(), meta, fetch, DefaultFingerprinter{})
	require.NoError(t, err)

	// Path must be <dir>/<digest.Encoded()>.
	require.Equal(t, filepath.Join(dir, d.Encoded()), entry.Path)

	// Spool file bytes must equal the source bytes.
	got, err := os.ReadFile(entry.Path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(gzBytes, got), "spool file differs from source")

	// Fingerprint must be non-empty (gzip+tar has one regular file).
	require.NotNil(t, entry.Fingerprint)
	require.NotEmpty(t, entry.Fingerprint)
}

// partialReadingFingerprinter consumes exactly n bytes of r and returns an
// empty Fingerprint. Used to prove TeeReader's spool file remains byte-identical
// to the source even when the fingerprinter stops early — the regression target
// for the unconditional drain in streamToSpool.
type partialReadingFingerprinter struct{ n int }

func (p partialReadingFingerprinter) Fingerprint(_ context.Context, _ string, blob []byte) (Fingerprint, error) {
	if len(blob) > p.n {
		blob = blob[:p.n]
	}
	_ = blob
	return Fingerprint{}, nil
}

func (p partialReadingFingerprinter) FingerprintReader(_ context.Context, _ string, r io.Reader) (Fingerprint, error) {
	buf := make([]byte, p.n)
	_, _ = io.ReadFull(r, buf)
	return Fingerprint{}, nil
}

// TestBaselineSpool_SpoolFileIsByteIdenticalWhenFingerprinterStopsEarly proves
// that the unconditional drain in streamToSpool captures all source bytes even
// when the fingerprinter stops reading early. This test FAILS without the
// io.Copy(io.Discard, tee) drain because the spool file will be only n bytes.
func TestBaselineSpool_SpoolFileIsByteIdenticalWhenFingerprinterStopsEarly(t *testing.T) {
	// Build a payload bigger than the partial-read budget so the bug, if
	// present, leaves the spool file shorter than the source.
	const partial = 16
	src := bytes.Repeat([]byte("ABCD"), 1024) // 4096 bytes
	d := digest.FromBytes(src)

	dir := t.TempDir()
	spool := newBaselineSpool(dir)
	meta := BaselineLayerMeta{Digest: d, Size: int64(len(src)), MediaType: "application/octet-stream"}
	fetch := func(_ digest.Digest) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(src)), nil
	}

	entry, err := spool.GetOrSpool(context.Background(), meta, fetch, partialReadingFingerprinter{n: partial})
	require.NoError(t, err)

	got, err := os.ReadFile(entry.Path)
	require.NoError(t, err)
	require.Equal(t, len(src), len(got),
		"drain bug present: spool file is shorter than source — TeeReader did not see the bytes the fingerprinter never read")
	require.True(t, bytes.Equal(src, got), "spool file content must match source byte-for-byte")
}

func TestBaselineSpool_SingleflightCollapsesConcurrentFirstTouches(t *testing.T) {
	dir := t.TempDir()
	s := newBaselineSpool(dir)

	gzBytes, d := makeGzipTarBlob(t, []byte("concurrent"))
	meta := makeMeta(d, int64(len(gzBytes)))

	var fetchCalls atomic.Int64
	fetch := func(_ digest.Digest) (io.ReadCloser, error) {
		fetchCalls.Add(1)
		return io.NopCloser(bytes.NewReader(gzBytes)), nil
	}

	const N = 16
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.GetOrSpool(context.Background(), meta, fetch, DefaultFingerprinter{})
		}()
	}
	wg.Wait()
	require.Equal(t, int64(1), fetchCalls.Load(),
		"fetch called %d times under singleflight, want 1", fetchCalls.Load())
}

func TestBaselineSpool_FetchErrorRemovesPartialFileAndDoesNotCache(t *testing.T) {
	dir := t.TempDir()
	s := newBaselineSpool(dir)

	d := digest.Digest("sha256:" + strings.Repeat("a", 64))
	meta := makeMeta(d, 5)

	var fetchCalls atomic.Int64
	fetch := func(_ digest.Digest) (io.ReadCloser, error) {
		n := fetchCalls.Add(1)
		if n == 1 {
			// Return an error to simulate a transient fetch failure.
			return nil, io.ErrUnexpectedEOF
		}
		// Second call returns a valid (empty) blob — still a real spool file.
		return io.NopCloser(bytes.NewReader([]byte{})), nil
	}

	// First call: fetch error — must propagate.
	_, err := s.GetOrSpool(context.Background(), meta, fetch, DefaultFingerprinter{})
	require.Error(t, err)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)

	// Partial file must be absent after the failure.
	partialPath := filepath.Join(dir, d.Encoded())
	_, statErr := os.Stat(partialPath)
	require.True(t, os.IsNotExist(statErr), "partial spool file must be removed on fetch error")

	// Second call: must succeed (no cache poisoning).
	entry, err := s.GetOrSpool(context.Background(), meta, fetch, DefaultFingerprinter{})
	require.NoError(t, err)
	require.Equal(t, partialPath, entry.Path)
}

// errAfterNReader returns n bytes then returns err on the next read.
type errAfterNReader struct {
	buf []byte
	err error
}

func (r *errAfterNReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		return 0, r.err
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *errAfterNReader) Close() error { return nil }

// TestBaselineSpool_MidStreamErrorRemovesPartialFile proves that the
// committed-gated cleanup defer in streamToSpool removes the partial spool file
// when a mid-stream read error occurs. This test FAILS if the
// _ = os.Remove(path) line in the defer is removed or disabled.
func TestBaselineSpool_MidStreamErrorRemovesPartialFile(t *testing.T) {
	dir := t.TempDir()
	spool := newBaselineSpool(dir)

	src := bytes.Repeat([]byte("X"), 4096)
	d := digest.FromBytes(src)
	meta := BaselineLayerMeta{Digest: d, Size: int64(len(src)), MediaType: "application/octet-stream"}

	boom := errors.New("boom: simulated read failure")
	fetch := func(_ digest.Digest) (io.ReadCloser, error) {
		// Return some bytes (so os.Create + first Read succeed) then error,
		// forcing streamToSpool past the file-creation point and into the
		// committed=false cleanup defer.
		return &errAfterNReader{buf: src[:128], err: boom}, nil
	}

	_, err := spool.GetOrSpool(context.Background(), meta, fetch, partialReadingFingerprinter{n: 64})
	require.Error(t, err)
	require.ErrorIs(t, err, boom, "underlying mid-stream error should propagate, got: %v", err)

	spoolPath := filepath.Join(dir, d.Encoded())
	_, statErr := os.Stat(spoolPath)
	require.True(t, os.IsNotExist(statErr),
		"partial spool file at %q must be removed by the committed=false cleanup defer (statErr=%v)", spoolPath, statErr)
}

// errFingerprinter returns an error on the first FingerprintReader call.
type errFingerprinter struct {
	callCount atomic.Int64
}

func (e *errFingerprinter) Fingerprint(
	ctx context.Context, mediaType string, blob []byte,
) (Fingerprint, error) {
	return e.FingerprintReader(ctx, mediaType, bytes.NewReader(blob))
}

func (e *errFingerprinter) FingerprintReader(
	_ context.Context, _ string, r io.Reader,
) (Fingerprint, error) {
	// Drain the reader so tee can capture all bytes — we're testing whether
	// the spool correctly handles fingerprint errors without truncating.
	_, drainErr := io.Copy(io.Discard, r)
	if drainErr != nil {
		return nil, drainErr
	}
	e.callCount.Add(1)
	return nil, errors.New("fingerprint intentionally failed")
}

func TestBaselineSpool_FingerprintErrorStillProducesIntactSpool(t *testing.T) {
	dir := t.TempDir()
	s := newBaselineSpool(dir)

	gzBytes, d := makeGzipTarBlob(t, []byte("fp error but spool ok"))
	meta := makeMeta(d, int64(len(gzBytes)))

	fetch := func(_ digest.Digest) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(gzBytes)), nil
	}

	fpErr := &errFingerprinter{}
	entry, err := s.GetOrSpool(context.Background(), meta, fetch, fpErr)
	require.NoError(t, err, "fingerprint error must not abort GetOrSpool")

	// Fingerprint nil is the sentinel for "failed fingerprint but blob is available".
	require.Nil(t, entry.Fingerprint)

	// Spool file must still be byte-identical to source.
	spoolBytes, err := os.ReadFile(entry.Path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(gzBytes, spoolBytes),
		"spool file must be intact even when fingerprinting fails")
}

func TestBaselineSpool_SnapshotFingerprints(t *testing.T) {
	dir := t.TempDir()
	s := newBaselineSpool(dir)

	gzBytes, d := makeGzipTarBlob(t, []byte("snapshot test"))
	meta := makeMeta(d, int64(len(gzBytes)))

	fetch := func(_ digest.Digest) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(gzBytes)), nil
	}

	_, err := s.GetOrSpool(context.Background(), meta, fetch, DefaultFingerprinter{})
	require.NoError(t, err)

	snap := s.SnapshotFingerprints()
	require.Contains(t, snap, d)
	require.NotNil(t, snap[d])
}
