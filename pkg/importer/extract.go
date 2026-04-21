package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"

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

func (b *extractedBundle) blobPath(d digest.Digest) string {
	parts := strings.SplitN(string(d), ":", 2)
	if len(parts) != 2 {
		return filepath.Join(b.blobDir, string(d))
	}
	return filepath.Join(b.blobDir, parts[0], parts[1])
}
