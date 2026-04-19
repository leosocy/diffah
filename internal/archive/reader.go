package archive

import (
	"archive/tar"
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"

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
		if err == io.EOF {
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

		target := filepath.Join(dest, hdr.Name)
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, fmt.Errorf("mkdir %s: %w", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir parent %s: %w", target, err)
		}
		if err := writeFile(target, tr, hdr.Size); err != nil {
			return nil, err
		}
	}

	if sidecar == nil {
		return nil, fmt.Errorf("archive %s missing %s", archivePath, diff.SidecarFilename)
	}
	return sidecar, nil
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
		if err == io.EOF {
			return nil, fmt.Errorf("archive %s missing %s", archivePath, diff.SidecarFilename)
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
	if _, err := io.CopyN(f, r, size); err != nil && err != io.EOF {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// openDecompressed sniffs the magic bytes and returns a ready-to-read
// stream. The returned closer, if non-nil, must be called when done.
func openDecompressed(f *os.File) (io.Reader, func(), error) {
	br := bufio.NewReader(f)
	magic, err := br.Peek(4)
	if err != nil && err != io.EOF {
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
