package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/directory"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
)

type composedImage struct {
	Name    string
	Ref     types.ImageReference
	tmpDir  string
}

func composeImage(
	ctx context.Context,
	img diff.ImageEntry,
	sc *diff.Sidecar,
	bundle *extractedBundle,
	baselineRef types.ImageReference,
) (*composedImage, error) {
	tmpDir, err := os.MkdirTemp("", "diffah-compose-")
	if err != nil {
		return nil, fmt.Errorf("create compose dir: %w", err)
	}

	if err := writeBlobToDir(tmpDir, img.Target.ManifestDigest, bundle); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	configDigest, err := extractConfigDigest(tmpDir, img.Target.ManifestDigest)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("extract config digest: %w", err)
	}

	if err := writeBlobToDir(tmpDir, configDigest, bundle); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("write config: %w", err)
	}

	for d, entry := range sc.Blobs {
		if d == img.Target.ManifestDigest || d == configDigest {
			continue
		}
		if entry.Encoding == diff.EncodingFull {
			if err := writeBlobToDir(tmpDir, d, bundle); err != nil {
				os.RemoveAll(tmpDir)
				return nil, fmt.Errorf("write full blob %s: %w", d, err)
			}
		} else if entry.Encoding == diff.EncodingPatch {
			if err := applyPatchAndWrite(ctx, tmpDir, d, entry, bundle, baselineRef); err != nil {
				os.RemoveAll(tmpDir)
				return nil, fmt.Errorf("apply patch %s: %w", d, err)
			}
		}
	}

	ref, err := directory.NewReference(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("create dir ref: %w", err)
	}

	return &composedImage{Name: img.Name, Ref: ref, tmpDir: tmpDir}, nil
}

func (ci *composedImage) cleanup() {
	os.RemoveAll(ci.tmpDir)
}

func writeBlobToDir(dir string, d digest.Digest, bundle *extractedBundle) error {
	srcPath := blobFilePath(bundle.tmpDir, d)
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read blob %s: %w", d, err)
	}
	parts := strings.SplitN(string(d), ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid digest %s", d)
	}
	blobDir := filepath.Join(dir, "blobs", parts[0])
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", blobDir, err)
	}
	return os.WriteFile(filepath.Join(blobDir, parts[1]), data, 0o644)
}

func blobFilePath(tmpDir string, d digest.Digest) string {
	parts := strings.SplitN(string(d), ":", 2)
	if len(parts) != 2 {
		return filepath.Join(tmpDir, string(d))
	}
	return filepath.Join(tmpDir, "blobs", parts[0], parts[1])
}

func extractConfigDigest(dir string, manifestDigest digest.Digest) (digest.Digest, error) {
	parts := strings.SplitN(string(manifestDigest), ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid digest %s", manifestDigest)
	}
	manifestPath := filepath.Join(dir, "blobs", parts[0], parts[1])
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read manifest: %w", err)
	}
	var m struct {
		Config struct {
			Digest digest.Digest `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("parse manifest: %w", err)
	}
	return m.Config.Digest, nil
}

func applyPatchAndWrite(
	ctx context.Context, dir string, d digest.Digest, entry diff.BlobEntry,
	bundle *extractedBundle, baselineRef types.ImageReference,
) error {
	patchData, err := os.ReadFile(blobFilePath(bundle.tmpDir, d))
	if err != nil {
		return fmt.Errorf("read patch %s: %w", d, err)
	}
	src, err := baselineRef.NewImageSource(ctx, nil)
	if err != nil {
		return fmt.Errorf("open baseline: %w", err)
	}
	defer src.Close()
	r, _, err := src.GetBlob(ctx, types.BlobInfo{Digest: entry.PatchFromDigest}, nil)
	if err != nil {
		return fmt.Errorf("get baseline blob %s: %w", entry.PatchFromDigest, err)
	}
	defer r.Close()
	baseData, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read baseline blob: %w", err)
	}
	recovered, err := applyPatch(entry.Codec, baseData, patchData)
	if err != nil {
		return fmt.Errorf("apply patch %s: %w", d, err)
	}
	parts := strings.SplitN(string(d), ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid digest %s", d)
	}
	blobDir := filepath.Join(dir, "blobs", parts[0])
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(blobDir, parts[1]), recovered, 0o644)
}

func applyPatch(codec string, base, patch []byte) ([]byte, error) {
	return zstdpatch.Decode(base, patch)
}
