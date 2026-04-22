package exporter

import (
	"archive/tar"
	"fmt"
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
	if err := writeTarEntry(tw, diff.SidecarFilename, scBytes); err != nil {
		return fmt.Errorf("write sidecar: %w", err)
	}

	for _, d := range pool.sortedDigests() {
		rel := blobPath(d)
		data, ok := pool.get(d)
		if !ok {
			return fmt.Errorf("blob %s missing from pool", d)
		}
		if err := writeTarEntry(tw, rel, data); err != nil {
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

func writeTarEntry(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name: name, Size: int64(len(data)), Mode: 0o644,
		Format: tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
