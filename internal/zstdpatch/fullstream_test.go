package zstdpatch

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestEncodeFullStream_ProducesValidZstdFrame(t *testing.T) {
	ctx := context.Background()
	target := []byte(repeat("hello world\n", 8192))

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target")
	mustWrite(t, targetPath, target)

	var buf bytes.Buffer
	gotSize, err := EncodeFullStream(ctx, targetPath, &buf, EncodeOpts{})
	if err != nil {
		t.Fatalf("EncodeFullStream: %v", err)
	}
	if int64(buf.Len()) != gotSize {
		t.Fatalf("size mismatch: returned %d, buf has %d", gotSize, buf.Len())
	}
	if gotSize <= 0 {
		t.Fatalf("expected non-zero encoded size")
	}

	// Round-trip via DecodeFull (klauspost decoder accepts both EncodeAll
	// and streaming Write+Close output).
	round, err := DecodeFull(buf.Bytes())
	if err != nil {
		t.Fatalf("DecodeFull(stream output): %v", err)
	}
	if !bytes.Equal(round, target) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestEncodeFullStream_SizeOnlyViaCountingWriter(t *testing.T) {
	// The actual use case for EncodeFullStream in the streaming pipeline:
	// pass a counting writer to measure the would-be output size without
	// materializing the encoded bytes anywhere.
	ctx := context.Background()
	target := []byte(repeat("abcd", 16384))

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target")
	mustWrite(t, targetPath, target)

	cw := &countingWriter{}
	size, err := EncodeFullStream(ctx, targetPath, cw, EncodeOpts{})
	if err != nil {
		t.Fatalf("EncodeFullStream: %v", err)
	}
	if size != cw.n {
		t.Fatalf("size %d != counting writer total %d", size, cw.n)
	}
	if size <= 0 {
		t.Fatalf("expected non-zero encoded size")
	}
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}
