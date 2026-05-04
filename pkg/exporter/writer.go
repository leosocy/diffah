package exporter

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

func writeBundleArchive(outPath string, sidecar diff.Sidecar, pool *blobPool) error {
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()

	scBytes, err := sidecar.Marshal()
	if err != nil {
		return fmt.Errorf("marshal sidecar: %w", err)
	}
	if err := writeTarEntryBytes(tw, diff.SidecarFilename, scBytes); err != nil {
		return fmt.Errorf("write sidecar: %w", err)
	}

	for _, d := range pool.sortedDigests() {
		spillPath, ok := pool.spills[d]
		if !ok {
			return fmt.Errorf("blob %s missing from pool", d)
		}
		if err := streamBlobIntoTar(tw, blobPath(d), spillPath); err != nil {
			return fmt.Errorf("write blob %s: %w", d, err)
		}
	}
	return nil
}

func blobPath(d digest.Digest) string {
	parts := strings.SplitN(string(d), ":", 2)
	if len(parts) != 2 {
		return filepath.Join("blobs", string(d))
	}
	return filepath.Join("blobs", parts[0], parts[1])
}

// writeTarEntryBytes writes an in-memory byte slice as a tar entry.
// Used for small sidecar payloads.
func writeTarEntryBytes(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name: name, Size: int64(len(data)), Mode: 0o644,
		Format: tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}
	_, err := tw.Write(data)
	return err
}

// streamBlobIntoTar opens the spill file at path, stats it for the tar
// header size, writes the header, then streams the content via io.Copy.
func streamBlobIntoTar(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open spill %s: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat spill %s: %w", path, err)
	}
	hdr := &tar.Header{
		Name: name, Size: fi.Size(), Mode: 0o644,
		Format: tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("copy spill %s: %w", path, err)
	}
	return nil
}
