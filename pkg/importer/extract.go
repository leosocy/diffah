package importer

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/pkg/diff"
)

type extractedBundle struct {
	tmpDir  string
	sidecar *diff.Sidecar
	blobDir string
	// sidecarRawBytes holds the on-disk diffah.json tar entry bytes
	// exactly as they appear in the archive. Preserved (not re-serialized
	// from the parsed *diff.Sidecar) so the canonical digest computed
	// during signature verification matches what the exporter signed.
	sidecarRawBytes []byte
}

func extractBundle(deltaPath, workdir string) (*extractedBundle, error) {
	tmpDir := filepath.Join(workdir, "bundle")
	if err := os.RemoveAll(tmpDir); err != nil {
		return nil, fmt.Errorf("reset bundle dir: %w", err)
	}
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return nil, fmt.Errorf("create bundle dir: %w", err)
	}
	raw, err := archive.Extract(deltaPath, tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("extract delta: %w", err)
	}
	sc, err := diff.ParseSidecar(raw)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("parse sidecar: %w", err)
	}
	return &extractedBundle{
		tmpDir:          tmpDir,
		sidecar:         sc,
		blobDir:         filepath.Join(tmpDir, "blobs"),
		sidecarRawBytes: raw,
	}, nil
}

func (b *extractedBundle) cleanup() {
	os.RemoveAll(b.tmpDir)
}
