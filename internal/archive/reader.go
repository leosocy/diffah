package archive

import (
	"archive/tar"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

// Extract writes every entry of the delta archive into dest and returns the
// sidecar bytes. dest must already exist and be empty.
func Extract(archivePath, dest string) ([]byte, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", archivePath, err)
	}
	defer f.Close()

	stream, closer, err := openDecompressed(f)
	if err != nil {
		return nil, err
	}
	if closer != nil {
		defer closer()
	}

	tr := tar.NewReader(stream)
	var sidecar []byte

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Name == diff.SidecarFilename {
			sidecar, err = io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read sidecar: %w", err)
			}
			continue
		}
		if err := extractEntry(tr, hdr, dest); err != nil {
			return nil, err
		}
	}

	if sidecar == nil {
		return nil, &diff.ErrNotADiffahArchive{Path: archivePath}
	}
	log().Debug("extracted archive", "path", archivePath, "dest", dest)
	return sidecar, nil
}

// extractEntry writes a single non-sidecar tar entry under dest after
// validating that its name cannot escape the destination directory.
func extractEntry(tr *tar.Reader, hdr *tar.Header, dest string) error {
	target, err := safeJoin(dest, hdr.Name)
	if err != nil {
		return err
	}
	if hdr.Typeflag == tar.TypeDir {
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", target, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("mkdir parent %s: %w", target, err)
	}
	return writeFile(target, tr, hdr.Size)
}

// safeJoin rejects archive entry names that are absolute or that would
// escape dest via "..", defending against zip slip.
func safeJoin(dest, name string) (string, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes destination", name)
	}
	return filepath.Join(dest, clean), nil
}

// ReadSidecar returns the sidecar bytes from a delta archive without
// extracting the rest of the entries.
func ReadSidecar(archivePath string) ([]byte, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", archivePath, err)
	}
	defer f.Close()

	stream, closer, err := openDecompressed(f)
	if err != nil {
		return nil, err
	}
	if closer != nil {
		defer closer()
	}

	tr := tar.NewReader(stream)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, &diff.ErrNotADiffahArchive{Path: archivePath}
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Name != diff.SidecarFilename {
			continue
		}
		return io.ReadAll(tr)
	}
}

func writeFile(path string, r io.Reader, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := io.CopyN(f, r, size); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// openDecompressed sniffs the magic bytes and returns a ready-to-read
// stream. The returned closer, if non-nil, must be called when done.
func openDecompressed(f *os.File) (io.Reader, func(), error) {
	br := bufio.NewReader(f)
	magic, err := br.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("peek: %w", err)
	}
	if bytes.Equal(magic, []byte{0x28, 0xB5, 0x2F, 0xFD}) {
		dec, err := zstd.NewReader(br)
		if err != nil {
			return nil, nil, fmt.Errorf("init zstd reader: %w", err)
		}
		return dec, dec.Close, nil
	}
	return br, nil, nil
}

// ReadSidecarAndManifestBlobs returns the sidecar bytes and the bytes of every
// blob in digests, in a single tar pass. Used by `diffah inspect` to enrich
// per-image output without extracting the full archive. Every digest in the
// argument MUST appear as a `blobs/<algo>/<encoded>` entry; missing-blob is
// an error. The returned map is keyed by digest, never nil. A nil or empty
// digests slice is equivalent to ReadSidecar and yields an empty blob map.
func ReadSidecarAndManifestBlobs(
	archivePath string, digests []digest.Digest,
) ([]byte, map[digest.Digest][]byte, error) {
	want := make(map[string]digest.Digest, len(digests))
	for _, d := range digests {
		want[blobTarPath(d)] = d
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", archivePath, err)
	}
	defer f.Close()

	stream, closer, err := openDecompressed(f)
	if err != nil {
		return nil, nil, err
	}
	if closer != nil {
		defer closer()
	}

	tr := tar.NewReader(stream)
	var sidecar []byte
	got := make(map[digest.Digest][]byte, len(digests))

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Name == diff.SidecarFilename {
			sidecar, err = io.ReadAll(tr)
			if err != nil {
				return nil, nil, fmt.Errorf("read sidecar: %w", err)
			}
			continue
		}
		if d, ok := want[hdr.Name]; ok {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, nil, fmt.Errorf("read %s: %w", hdr.Name, err)
			}
			got[d] = data
		}
	}

	if sidecar == nil {
		return nil, nil, &diff.ErrNotADiffahArchive{Path: archivePath}
	}
	for _, d := range digests {
		if _, ok := got[d]; !ok {
			return nil, nil, fmt.Errorf("blob %s not found in archive %s", d, archivePath)
		}
	}
	return sidecar, got, nil
}

// blobTarPath returns the in-archive tar entry name for a blob, matching
// the writer convention in pkg/exporter/writer.go. MUST stay in sync with
// pkg/exporter/writer.go:blobPath (no shared package to keep the
// internal/archive ← pkg/exporter dependency direction clean).
func blobTarPath(d digest.Digest) string {
	parts := strings.SplitN(string(d), ":", 2)
	if len(parts) != 2 {
		return filepath.Join("blobs", string(d))
	}
	return filepath.Join("blobs", parts[0], parts[1])
}
