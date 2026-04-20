package exporter

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestScore_DisjointFingerprints(t *testing.T) {
	a := Fingerprint{digest.FromBytes([]byte("one")): 100}
	b := Fingerprint{digest.FromBytes([]byte("two")): 200}
	require.Zero(t, score(a, b))
}

func TestScore_IdenticalFingerprints(t *testing.T) {
	d := digest.FromBytes([]byte("shared"))
	a := Fingerprint{d: 500}
	require.Equal(t, int64(500), score(a, a))
}

func TestScore_PartialOverlap(t *testing.T) {
	shared := digest.FromBytes([]byte("shared"))
	onlyA := digest.FromBytes([]byte("onlya"))
	onlyB := digest.FromBytes([]byte("onlyb"))
	a := Fingerprint{shared: 100, onlyA: 50}
	b := Fingerprint{shared: 100, onlyB: 200}
	require.Equal(t, int64(100), score(a, b))
}

func TestScore_EmptyTarget(t *testing.T) {
	var empty Fingerprint
	b := Fingerprint{digest.FromBytes([]byte("x")): 100}
	require.Zero(t, score(empty, b))
}

func TestScore_NilCandidate(t *testing.T) {
	a := Fingerprint{digest.FromBytes([]byte("x")): 100}
	require.Zero(t, score(a, nil))
}

func TestScore_NilTarget(t *testing.T) {
	b := Fingerprint{digest.FromBytes([]byte("x")): 100}
	require.Zero(t, score(nil, b))
}

// tarEntry is a test helper for building synthetic tar blobs.
type tarEntry struct {
	name     string
	data     []byte
	typeflag byte
	linkname string
}

func buildTarBlob(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Size:     int64(len(e.data)),
			Mode:     0o644,
			ModTime:  time.Unix(0, 0),
			Typeflag: e.typeflag,
			Linkname: e.linkname,
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if len(e.data) > 0 {
			_, err := tw.Write(e.data)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func TestDefaultFingerprinter_PlainTar_SingleFile(t *testing.T) {
	data := []byte("hello, diffah")
	blob := buildTarBlob(t, tarEntry{
		name: "a.txt", data: data, typeflag: tar.TypeReg,
	})

	fp, err := (DefaultFingerprinter{}).Fingerprint(
		context.Background(),
		"application/vnd.oci.image.layer.v1.tar",
		blob,
	)
	require.NoError(t, err)
	require.Equal(t,
		Fingerprint{digest.FromBytes(data): int64(len(data))},
		fp,
	)
}

func TestDefaultFingerprinter_SkipsNonRegular(t *testing.T) {
	blob := buildTarBlob(t,
		tarEntry{name: "dir/", typeflag: tar.TypeDir},
		tarEntry{name: "link", typeflag: tar.TypeSymlink, linkname: "target"},
		tarEntry{name: "hard", typeflag: tar.TypeLink, linkname: "other"},
		tarEntry{name: "fifo", typeflag: tar.TypeFifo},
	)
	fp, err := (DefaultFingerprinter{}).Fingerprint(
		context.Background(),
		"application/vnd.oci.image.layer.v1.tar",
		blob,
	)
	require.NoError(t, err)
	require.Empty(t, fp)
}

func TestDefaultFingerprinter_DedupByContent(t *testing.T) {
	data := []byte("duplicate")
	blob := buildTarBlob(t,
		tarEntry{name: "a", data: data, typeflag: tar.TypeReg},
		tarEntry{name: "b", data: data, typeflag: tar.TypeReg},
	)
	fp, err := (DefaultFingerprinter{}).Fingerprint(
		context.Background(),
		"application/vnd.oci.image.layer.v1.tar",
		blob,
	)
	require.NoError(t, err)
	require.Len(t, fp, 1)
	require.Equal(t, int64(len(data)), fp[digest.FromBytes(data)])
}

func TestDefaultFingerprinter_EmptyTar(t *testing.T) {
	blob := buildTarBlob(t)
	fp, err := (DefaultFingerprinter{}).Fingerprint(
		context.Background(),
		"application/vnd.oci.image.layer.v1.tar",
		blob,
	)
	require.NoError(t, err)
	require.Empty(t, fp)
}

func TestDefaultFingerprinter_TarParseFailure_WrapsErr(t *testing.T) {
	// Random bytes are not a valid tar; the tar reader must error and the
	// fingerprinter must wrap that error with ErrFingerprintFailed.
	_, err := (DefaultFingerprinter{}).Fingerprint(
		context.Background(),
		"application/vnd.oci.image.layer.v1.tar",
		[]byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB, 0xFA, 0xF9, 0xF8},
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrFingerprintFailed))
}

func TestDefaultFingerprinter_GzipTar(t *testing.T) {
	data := []byte("hello via gzip")
	tarBlob := buildTarBlob(t, tarEntry{
		name: "a", data: data, typeflag: tar.TypeReg,
	})
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	_, err := gw.Write(tarBlob)
	require.NoError(t, err)
	require.NoError(t, gw.Close())

	fp, err := (DefaultFingerprinter{}).Fingerprint(
		context.Background(),
		"application/vnd.oci.image.layer.v1.tar+gzip",
		gzBuf.Bytes(),
	)
	require.NoError(t, err)
	require.Equal(t,
		Fingerprint{digest.FromBytes(data): int64(len(data))},
		fp,
	)
}

func TestDefaultFingerprinter_TruncatedGzip_WrapsErr(t *testing.T) {
	tarBlob := buildTarBlob(t, tarEntry{
		name: "a", data: []byte("x"), typeflag: tar.TypeReg,
	})
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	_, _ = gw.Write(tarBlob)
	_ = gw.Close()
	// Truncate deflate payload (not just the 8-byte gzip trailer): gzip.Reader
	// validates CRC/ISIZE lazily, so a trailer-only truncation would let
	// tar.Reader reach io.EOF before the gzip layer surfaces an error.
	truncated := gzBuf.Bytes()[:len(gzBuf.Bytes())-20]

	_, err := (DefaultFingerprinter{}).Fingerprint(
		context.Background(),
		"application/vnd.oci.image.layer.v1.tar+gzip",
		truncated,
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrFingerprintFailed))
}

func TestDefaultFingerprinter_CorruptGzipHeader_WrapsErr(t *testing.T) {
	// Valid gzip magic is 0x1F 0x8B. Replacing it forces gzip.NewReader
	// to fail at construction — exercising openDecompressor's gzip
	// error-wrapping branch, distinct from the truncated-deflate path
	// which surfaces from fingerprintTar.
	corrupt := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, err := (DefaultFingerprinter{}).Fingerprint(
		context.Background(),
		"application/vnd.oci.image.layer.v1.tar+gzip",
		corrupt,
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrFingerprintFailed))
}

func TestDefaultFingerprinter_ZstdTar(t *testing.T) {
	data := []byte("hello via zstd")
	tarBlob := buildTarBlob(t, tarEntry{
		name: "a", data: data, typeflag: tar.TypeReg,
	})
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	zstdBlob := enc.EncodeAll(tarBlob, nil)
	require.NoError(t, enc.Close())

	fp, err := (DefaultFingerprinter{}).Fingerprint(
		context.Background(),
		"application/vnd.oci.image.layer.v1.tar+zstd",
		zstdBlob,
	)
	require.NoError(t, err)
	require.Equal(t,
		Fingerprint{digest.FromBytes(data): int64(len(data))},
		fp,
	)
}

func TestDefaultFingerprinter_TruncatedZstd_WrapsErr(t *testing.T) {
	tarBlob := buildTarBlob(t, tarEntry{
		name: "a", data: []byte("x"), typeflag: tar.TypeReg,
	})
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	zstdBlob := enc.EncodeAll(tarBlob, nil)
	_ = enc.Close()
	truncated := zstdBlob[:len(zstdBlob)/2]

	_, err = (DefaultFingerprinter{}).Fingerprint(
		context.Background(),
		"application/vnd.oci.image.layer.v1.tar+zstd",
		truncated,
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrFingerprintFailed))
}

func TestDefaultFingerprinter_CorruptZstdHeader_WrapsErr(t *testing.T) {
	// Valid zstd magic is 0x28 0xB5 0x2F 0xFD. Unlike gzip.NewReader,
	// klauspost's NewReader is lazy — construction succeeds on garbage
	// input and the error surfaces on the first Read. So this test
	// exercises the same fingerprintTar error path as TruncatedZstd,
	// not openDecompressor's construction-error branch. Kept for parity
	// with the gzip counterpart and as a guard if library semantics
	// change.
	corrupt := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, err := (DefaultFingerprinter{}).Fingerprint(
		context.Background(),
		"application/vnd.oci.image.layer.v1.tar+zstd",
		corrupt,
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrFingerprintFailed))
}
