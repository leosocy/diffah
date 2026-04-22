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
}

func extractBundle(deltaPath string) (*extractedBundle, error) {
	tmpDir, err := os.MkdirTemp("", "diffah-import-")
	if err != nil {
		return nil, fmt.Errorf("create tmp dir: %w", err)
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
		tmpDir:  tmpDir,
		sidecar: sc,
		blobDir: filepath.Join(tmpDir, "blobs"),
	}, nil
}

func (b *extractedBundle) cleanup() {
	os.RemoveAll(b.tmpDir)
}
