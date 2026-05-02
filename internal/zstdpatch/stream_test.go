package zstdpatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestEncodeStream_ParityWithLegacyEncode(t *testing.T) {
	skipWithoutZstd(t)
	ctx := context.Background()
	ref := []byte("the quick brown fox jumps over the lazy dog\n" + repeat("X", 4096))
	target := []byte("the quick brown FOX jumps over the lazy dog!\n" + repeat("X", 4096))

	dir := t.TempDir()
	refPath := filepath.Join(dir, "ref")
	targetPath := filepath.Join(dir, "target")
	if err := os.WriteFile(refPath, ref, 0o600); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	if err := os.WriteFile(targetPath, target, 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	outPath := filepath.Join(dir, "patch.zst")

	gotSize, err := EncodeStream(ctx, refPath, targetPath, outPath, EncodeOpts{})
	if err != nil {
		t.Fatalf("EncodeStream: %v", err)
	}
	gotBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read patch: %v", err)
	}
	if int64(len(gotBytes)) != gotSize {
		t.Fatalf("size mismatch: returned %d, file has %d", gotSize, len(gotBytes))
	}

	wantBytes, err := Encode(ctx, ref, target, EncodeOpts{})
	if err != nil {
		t.Fatalf("legacy Encode: %v", err)
	}
	if hash(gotBytes) != hash(wantBytes) {
		t.Fatalf("EncodeStream output differs from Encode: stream=%s legacy=%s",
			hash(gotBytes), hash(wantBytes))
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestDecodeStream_RoundTripsEncodeStream(t *testing.T) {
	skipWithoutZstd(t)
	ctx := context.Background()
	ref := []byte(repeat("alpha", 4096))
	target := []byte(repeat("alpha", 4096) + "_delta_suffix")

	dir := t.TempDir()
	refPath := filepath.Join(dir, "ref")
	targetPath := filepath.Join(dir, "target")
	patchPath := filepath.Join(dir, "patch.zst")
	decodedPath := filepath.Join(dir, "decoded")

	mustWrite(t, refPath, ref)
	mustWrite(t, targetPath, target)

	if _, err := EncodeStream(ctx, refPath, targetPath, patchPath, EncodeOpts{}); err != nil {
		t.Fatalf("EncodeStream: %v", err)
	}
	if _, err := DecodeStream(ctx, refPath, patchPath, decodedPath); err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	got, err := os.ReadFile(decodedPath)
	if err != nil {
		t.Fatalf("read decoded: %v", err)
	}
	if hash(got) != hash(target) {
		t.Fatalf("decoded != target")
	}
}

func TestEncodeStream_EmptyTargetEmitsEmptyFrame(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	refPath := filepath.Join(dir, "ref")
	targetPath := filepath.Join(dir, "target")
	patchPath := filepath.Join(dir, "patch.zst")
	mustWrite(t, refPath, []byte("anything"))
	mustWrite(t, targetPath, nil)

	size, err := EncodeStream(ctx, refPath, targetPath, patchPath, EncodeOpts{})
	if err != nil {
		t.Fatalf("EncodeStream: %v", err)
	}
	got, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("read patch: %v", err)
	}
	if !bytesEqualPublic(got, emptyZstdFrame()) {
		t.Fatalf("expected empty frame, got %d bytes", len(got))
	}
	if size != int64(len(emptyZstdFrame())) {
		t.Fatalf("size mismatch")
	}
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func bytesEqualPublic(a, b []byte) bool { return bytesEqual(a, b) }
