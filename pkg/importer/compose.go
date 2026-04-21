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

	manifestData, err := readBlobFromBundle(bundle, img.Target.ManifestDigest)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "manifest.json"), manifestData, 0o644); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	configDigest, err := extractConfigDigestFromBytes(manifestData)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("extract config digest: %w", err)
	}

	if err := writeBlobAsDigestFile(tmpDir, configDigest, bundle); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("write config: %w", err)
	}

	for d, entry := range sc.Blobs {
		if d == img.Target.ManifestDigest || d == configDigest {
			continue
		}
		if entry.Encoding == diff.EncodingFull {
			if err := writeBlobAsDigestFile(tmpDir, d, bundle); err != nil {
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

	layerDigests, err := extractLayerDigests(manifestData)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("extract layer digests: %w", err)
	}
	for _, ld := range layerDigests {
		if _, ok := sc.Blobs[ld]; ok {
			continue
		}
		if ld == configDigest {
			continue
		}
		if err := fetchBaselineBlob(tmpDir, ctx, ld, baselineRef); err != nil {
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("fetch baseline blob %s: %w", ld, err)
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

func readBlobFromBundle(bundle *extractedBundle, d digest.Digest) ([]byte, error) {
	srcPath := blobFilePath(bundle.tmpDir, d)
	return os.ReadFile(srcPath)
}

func writeBlobAsDigestFile(dir string, d digest.Digest, bundle *extractedBundle) error {
	data, err := readBlobFromBundle(bundle, d)
	if err != nil {
		return fmt.Errorf("read blob %s: %w", d, err)
	}
	parts := strings.SplitN(string(d), ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid digest %s", d)
	}
	return os.WriteFile(filepath.Join(dir, parts[1]), data, 0o644)
}

func blobFilePath(tmpDir string, d digest.Digest) string {
	parts := strings.SplitN(string(d), ":", 2)
	if len(parts) != 2 {
		return filepath.Join(tmpDir, string(d))
	}
	return filepath.Join(tmpDir, "blobs", parts[0], parts[1])
}

func extractConfigDigestFromBytes(manifestData []byte) (digest.Digest, error) {
	var m struct {
		Config struct {
			Digest digest.Digest `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(manifestData, &m); err != nil {
		return "", fmt.Errorf("parse manifest: %w", err)
	}
	return m.Config.Digest, nil
}

func extractLayerDigests(manifestData []byte) ([]digest.Digest, error) {
	var m struct {
		Layers []struct {
			Digest digest.Digest `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(manifestData, &m); err != nil {
		return nil, fmt.Errorf("parse manifest layers: %w", err)
	}
	out := make([]digest.Digest, 0, len(m.Layers))
	for _, l := range m.Layers {
		out = append(out, l.Digest)
	}
	return out, nil
}

func fetchBaselineBlob(dir string, ctx context.Context, d digest.Digest, baselineRef types.ImageReference) error {
	src, err := baselineRef.NewImageSource(ctx, nil)
	if err != nil {
		return fmt.Errorf("open baseline: %w", err)
	}
	defer src.Close()
	r, _, err := src.GetBlob(ctx, types.BlobInfo{Digest: d}, nil)
	if err != nil {
		return fmt.Errorf("get baseline blob %s: %w", d, err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read baseline blob %s: %w", d, err)
	}
	parts := strings.SplitN(string(d), ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid digest %s", d)
	}
	return os.WriteFile(filepath.Join(dir, parts[1]), data, 0o644)
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
	return os.WriteFile(filepath.Join(dir, parts[1]), recovered, 0o644)
}

func applyPatch(codec string, base, patch []byte) ([]byte, error) {
	return zstdpatch.Decode(base, patch)
}
