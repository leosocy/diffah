package archive

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"

	"github.com/leosocy/diffah/pkg/diff"
)

// Compression is the outer wrapper applied to the tar stream.
type Compression string

const (
	CompressNone Compression = "none"
	CompressZstd Compression = "zstd"
)

// Pack writes a tar archive containing every file under srcDir plus the
// sidecar payload as "diffah.json" at the archive root.
//
// Pack writes to outPath+".tmp" first and renames on success, guaranteeing
// that observers never see a partial file at outPath.
func Pack(srcDir string, sidecar []byte, outPath string, c Compression) error {
	tmp := outPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	defer f.Close()

	var stream io.Writer = f
	var zw *zstd.Encoder
	if c == CompressZstd {
		zw, err = zstd.NewWriter(f)
		if err != nil {
			return fmt.Errorf("init zstd writer: %w", err)
		}
		stream = zw
	}

	tw := tar.NewWriter(stream)

	if err := addDir(tw, srcDir); err != nil {
		return err
	}
	if err := addBytes(tw, diff.SidecarFilename, sidecar); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar writer: %w", err)
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			return fmt.Errorf("close zstd writer: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, outPath); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, outPath, err)
	}
	return nil
}

func addDir(tw *tar.Writer, root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		if info.IsDir() {
			if path == root {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			hdr := &tar.Header{
				Name:     rel + "/",
				Mode:     0o755,
				Typeflag: tar.TypeDir,
			}
			return tw.WriteHeader(hdr)
		}
		return addFile(tw, root, path, info)
	})
}

func addFile(tw *tar.Writer, root, path string, info os.FileInfo) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	hdr := &tar.Header{
		Name: rel,
		Mode: 0o644,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", rel, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write tar body %s: %w", rel, err)
	}
	return nil
}

func addBytes(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
