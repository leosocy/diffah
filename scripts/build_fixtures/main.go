//go:build containers_image_openpgp

// Command build_fixtures produces deterministic OCI and Docker Schema 2
// archive fixtures used by diffah tests. Run with:
//
//	go run -tags containers_image_openpgp ./scripts/build_fixtures
//
// Outputs: testdata/fixtures/v{1,2,3}_oci.tar, v{1,2,3}_s2.tar, unrelated_oci.tar, CHECKSUMS
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/opencontainers/go-digest"
	dockerarchive "go.podman.io/image/v5/docker/archive"
	dockerref "go.podman.io/image/v5/docker/reference"
	ociarchive "go.podman.io/image/v5/oci/archive"
	"go.podman.io/image/v5/pkg/blobinfocache/none"
	"go.podman.io/image/v5/types"
)

// pinnedTime is the fixed timestamp used for all tar headers and image config.
var pinnedTime = time.Unix(1700000000, 0)

// fixtureDir is the output directory for all fixture files.
const fixtureDir = "testdata/fixtures"

// Media types for OCI and Docker Schema 2.
const (
	ociLayerMediaType  = "application/vnd.oci.image.layer.v1.tar+gzip"
	ociConfigMediaType = "application/vnd.oci.image.config.v1+json"
	ociManifestMT      = "application/vnd.oci.image.manifest.v1+json"
	s2LayerMediaType   = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	s2ConfigMediaType  = "application/vnd.docker.container.image.v1+json"
	s2ManifestMT       = "application/vnd.docker.distribution.manifest.v2+json"
)

// buildLayerBlob creates a gzipped tar containing a single file.
// Returns (compressed bytes, diffID of uncompressed tar, digest of compressed bytes).
// All tar headers and gzip fields are set to pinned values for determinism.
func buildLayerBlob(filename string, data []byte) ([]byte, digest.Digest, digest.Digest) {
	// Build uncompressed tar first to compute diffID.
	rawBuf := &bytes.Buffer{}
	tw := tar.NewWriter(rawBuf)
	hdr := &tar.Header{
		Name:     filename,
		Size:     int64(len(data)),
		Mode:     0o644,
		ModTime:  pinnedTime,
		Typeflag: tar.TypeReg,
		Uid:      0,
		Gid:      0,
		Uname:    "",
		Gname:    "",
	}
	if err := tw.WriteHeader(hdr); err != nil {
		panic(fmt.Sprintf("write tar header: %v", err))
	}
	if _, err := tw.Write(data); err != nil {
		panic(fmt.Sprintf("write tar data: %v", err))
	}
	if err := tw.Close(); err != nil {
		panic(fmt.Sprintf("close tar writer: %v", err))
	}
	rawBytes := rawBuf.Bytes()
	diffID := digest.FromBytes(rawBytes)

	// Now gzip the uncompressed tar with deterministic gzip headers.
	compBuf := &bytes.Buffer{}
	gz, err := gzip.NewWriterLevel(compBuf, gzip.NoCompression)
	if err != nil {
		panic(fmt.Sprintf("new gzip writer: %v", err))
	}
	// Zero out all variable gzip header fields for determinism.
	gz.ModTime = time.Time{}
	gz.OS = 0xFF // "unknown"
	gz.Name = ""
	gz.Comment = ""
	gz.Extra = nil

	if _, err := gz.Write(rawBytes); err != nil {
		panic(fmt.Sprintf("write gzip data: %v", err))
	}
	if err := gz.Close(); err != nil {
		panic(fmt.Sprintf("close gzip writer: %v", err))
	}
	compBytes := compBuf.Bytes()
	blobDigest := digest.FromBytes(compBytes)

	return compBytes, diffID, blobDigest
}

// imageConfig is the JSON-serializable image configuration structure.
type imageConfig struct {
	Architecture string         `json:"architecture"`
	OS           string         `json:"os"`
	Created      string         `json:"created"`
	RootFS       rootfsConfig   `json:"rootfs"`
	Config       map[string]any `json:"config"`
	History      []any          `json:"history"`
}

type rootfsConfig struct {
	Type    string   `json:"type"`
	DiffIDs []string `json:"diff_ids"`
}

// buildConfig returns the image config JSON bytes.
// Both OCI and Docker Schema 2 use the same structure; the mediaType is used
// only by the caller to set BlobInfo correctly.
func buildConfig(diffIDs []digest.Digest) []byte {
	ids := make([]string, len(diffIDs))
	for i, d := range diffIDs {
		ids[i] = d.String()
	}
	cfg := imageConfig{
		Architecture: "amd64",
		OS:           "linux",
		Created:      pinnedTime.UTC().Format(time.RFC3339),
		RootFS: rootfsConfig{
			Type:    "layers",
			DiffIDs: ids,
		},
		Config:  map[string]any{},
		History: []any{},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		panic(fmt.Sprintf("marshal config: %v", err))
	}
	return b
}

// manifestLayer is a JSON-serializable layer or config descriptor.
type manifestDescriptor struct {
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"`
}

// ociManifest is the JSON structure for an OCI image manifest.
type ociManifest struct {
	SchemaVersion int                  `json:"schemaVersion"`
	MediaType     string               `json:"mediaType"`
	Config        manifestDescriptor   `json:"config"`
	Layers        []manifestDescriptor `json:"layers"`
}

// buildManifest returns the manifest JSON bytes for the given format.
func buildManifest(
	configInfo types.BlobInfo,
	layerInfos []types.BlobInfo,
	manifestMediaType string,
	layerMediaType string,
	configMediaType string,
) []byte {
	layers := make([]manifestDescriptor, len(layerInfos))
	for i, li := range layerInfos {
		layers[i] = manifestDescriptor{
			MediaType: layerMediaType,
			Size:      li.Size,
			Digest:    li.Digest.String(),
		}
	}
	m := ociManifest{
		SchemaVersion: 2,
		MediaType:     manifestMediaType,
		Config: manifestDescriptor{
			MediaType: configMediaType,
			Size:      configInfo.Size,
			Digest:    configInfo.Digest.String(),
		},
		Layers: layers,
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("marshal manifest: %v", err))
	}
	return b
}

// writeFixture creates the image destination, puts all blobs, manifest, and commits.
func writeFixture(
	ctx context.Context,
	destRef types.ImageReference,
	layers [][]byte,
	layerInfos []types.BlobInfo,
	configJSON []byte,
	manifestBytes []byte,
) error {
	dest, err := destRef.NewImageDestination(ctx, nil)
	if err != nil {
		return fmt.Errorf("new image destination: %w", err)
	}
	defer dest.Close()

	cache := none.NoCache

	// Put each layer blob.
	for i, lb := range layers {
		_, err := dest.PutBlob(ctx, bytes.NewReader(lb), layerInfos[i], cache, false)
		if err != nil {
			return fmt.Errorf("put layer %d: %w", i, err)
		}
	}

	// Put config blob.
	h := sha256.New()
	h.Write(configJSON)
	configDigest := digest.NewDigestFromEncoded(digest.SHA256, fmt.Sprintf("%x", h.Sum(nil)))
	configInfo := types.BlobInfo{
		Digest: configDigest,
		Size:   int64(len(configJSON)),
	}
	if _, err := dest.PutBlob(ctx, bytes.NewReader(configJSON), configInfo, cache, true); err != nil {
		return fmt.Errorf("put config: %w", err)
	}

	// Put manifest.
	if err := dest.PutManifest(ctx, manifestBytes, nil); err != nil {
		return fmt.Errorf("put manifest: %w", err)
	}

	// PutSignatures is required for Docker archive destinations.
	if err := dest.PutSignatures(ctx, nil, nil); err != nil {
		return fmt.Errorf("put signatures: %w", err)
	}

	// Commit — pass nil for unparsedToplevel; both archive types ignore it.
	if err := dest.Commit(ctx, nil); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// normalizeTar rewrites the tar at path so that every header has pinned
// timestamps, zero UID/GID, no uname/gname, and entries are sorted by name.
// This ensures bit-identical outer archives across runs regardless of how the
// underlying library lays them out on disk.
func normalizeTar(path string) error {
	// Read all entries from the existing archive.
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open tar for normalization: %w", err)
	}
	type entry struct {
		hdr  *tar.Header
		data []byte
	}
	var entries []entry
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			f.Close()
			return fmt.Errorf("read tar entry: %w", err)
		}
		// Normalize header.
		hdr.ModTime = pinnedTime
		hdr.ChangeTime = time.Time{}
		hdr.AccessTime = time.Time{}
		hdr.Uid = 0
		hdr.Gid = 0
		hdr.Uname = ""
		hdr.Gname = ""
		hdr.Xattrs = nil //nolint:staticcheck // intentional clear
		hdr.PAXRecords = nil
		data, err := io.ReadAll(tr)
		if err != nil {
			f.Close()
			return fmt.Errorf("read tar data: %w", err)
		}
		entries = append(entries, entry{hdr, data})
	}
	f.Close()

	// Sort entries by name for deterministic ordering.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].hdr.Name < entries[j].hdr.Name
	})

	// Write normalized archive back to the same path.
	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create normalized tar: %w", err)
	}
	defer out.Close()
	tw := tar.NewWriter(out)
	for _, e := range entries {
		if err := tw.WriteHeader(e.hdr); err != nil {
			return fmt.Errorf("write normalized header: %w", err)
		}
		if _, err := tw.Write(e.data); err != nil {
			return fmt.Errorf("write normalized data: %w", err)
		}
	}
	return tw.Close()
}

// fileChecksum computes the sha256 checksum of the file at path.
func fileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// removeIfExists removes the file at path if it exists; ignores NotExist errors.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// seededRandom returns size bytes of deterministic pseudo-random data from the given seed.
func seededRandom(seed int64, size int) []byte {
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic fixture data
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(rng.Intn(256))
	}
	return buf
}

// buildFixtures builds all fixture archives and writes CHECKSUMS.
func buildFixtures(ctx context.Context) error {
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		return fmt.Errorf("mkdir fixtures: %w", err)
	}

	// Remove any pre-existing fixture files so transports can create fresh archives.
	for _, name := range []string{"v1_oci.tar", "v1_s2.tar", "v2_oci.tar", "v2_s2.tar", "v3_oci.tar", "v3_s2.tar", "unrelated_oci.tar", "CHECKSUMS"} {
		if err := removeIfExists(filepath.Join(fixtureDir, name)); err != nil {
			return err
		}
	}

	// Build shared base layer for v1 and v2: /shared.bin = 1 MiB pseudo-random data.
	// Using random data ensures zstd cannot collapse content with RLE, so patch-from
	// between v2 and v3 (which differ by 1 byte) produces a meaningfully small patch.
	baseData := seededRandom(42, 1<<20)
	baseCompressed, baseDiffID, baseBlob := buildLayerBlob("shared.bin", baseData)
	fmt.Printf("shared layer (v1/v2) diffID: %s\n", baseDiffID)
	fmt.Printf("shared layer (v1/v2) blobDigest: %s\n", baseBlob)

	// Build v3 base layer: same as v1/v2 base but with 1 byte flipped.
	v3BaseData := make([]byte, len(baseData))
	copy(v3BaseData, baseData)
	v3BaseData[0] ^= 0x01
	v3BaseCompressed, v3BaseDiffID, v3BaseBlob := buildLayerBlob("shared.bin", v3BaseData)
	fmt.Printf("v3 shared layer diffID: %s\n", v3BaseDiffID)
	fmt.Printf("v3 shared layer blobDigest: %s\n", v3BaseBlob)

	// Build version layers.
	v1Compressed, v1DiffID, v1Blob := buildLayerBlob("version.txt", []byte("v1\n"))
	v2Compressed, v2DiffID, v2Blob := buildLayerBlob("version.txt", []byte("v2\n"))
	v3Compressed, v3DiffID, v3Blob := buildLayerBlob("version.txt", []byte("v3\n"))

	type fixtureSpec struct {
		name          string
		baseLayer     []byte
		baseDiff      digest.Digest
		baseBlob      digest.Digest
		versionLayer  []byte
		versionDiff   digest.Digest
		versionBlob   digest.Digest
		version       string
	}

	versions := []fixtureSpec{
		{
			name:         "v1",
			baseLayer:    baseCompressed,
			baseDiff:     baseDiffID,
			baseBlob:     baseBlob,
			versionLayer: v1Compressed,
			versionDiff:  v1DiffID,
			versionBlob:  v1Blob,
			version:      "v1",
		},
		{
			name:         "v2",
			baseLayer:    baseCompressed,
			baseDiff:     baseDiffID,
			baseBlob:     baseBlob,
			versionLayer: v2Compressed,
			versionDiff:  v2DiffID,
			versionBlob:  v2Blob,
			version:      "v2",
		},
		{
			name:         "v3",
			baseLayer:    v3BaseCompressed,
			baseDiff:     v3BaseDiffID,
			baseBlob:     v3BaseBlob,
			versionLayer: v3Compressed,
			versionDiff:  v3DiffID,
			versionBlob:  v3Blob,
			version:      "v3",
		},
	}

	type archiveOutput struct {
		filename string
		checksum string
	}
	var outputs []archiveOutput

	for _, vs := range versions {
		layers := [][]byte{vs.baseLayer, vs.versionLayer}
		layerDiffs := []digest.Digest{vs.baseDiff, vs.versionDiff}
		layerBlobs := []types.BlobInfo{
			{Digest: vs.baseBlob, Size: int64(len(vs.baseLayer))},
			{Digest: vs.versionBlob, Size: int64(len(vs.versionLayer))},
		}

		configJSON := buildConfig(layerDiffs)
		h := sha256.New()
		h.Write(configJSON)
		configDigest := digest.NewDigestFromEncoded(digest.SHA256, fmt.Sprintf("%x", h.Sum(nil)))
		configInfo := types.BlobInfo{
			Digest: configDigest,
			Size:   int64(len(configJSON)),
		}

		// --- OCI archive ---
		ociPath := filepath.Join(fixtureDir, vs.name+"_oci.tar")
		ociLayerInfos := make([]types.BlobInfo, len(layerBlobs))
		for i, li := range layerBlobs {
			ociLayerInfos[i] = types.BlobInfo{
				Digest:    li.Digest,
				Size:      li.Size,
				MediaType: ociLayerMediaType,
			}
		}
		ociManifestBytes := buildManifest(configInfo, ociLayerInfos, ociManifestMT, ociLayerMediaType, ociConfigMediaType)
		ociRef, err := ociarchive.NewReference(ociPath, "diffah-fixture:"+vs.version)
		if err != nil {
			return fmt.Errorf("oci ref %s: %w", vs.name, err)
		}
		if err := writeFixture(ctx, ociRef, layers, ociLayerInfos, configJSON, ociManifestBytes); err != nil {
			return fmt.Errorf("write oci fixture %s: %w", vs.name, err)
		}
		if err := normalizeTar(ociPath); err != nil {
			return fmt.Errorf("normalize oci tar %s: %w", vs.name, err)
		}
		fmt.Printf("wrote %s\n", ociPath)

		// --- Docker Schema 2 archive ---
		s2Path := filepath.Join(fixtureDir, vs.name+"_s2.tar")
		s2LayerInfos := make([]types.BlobInfo, len(layerBlobs))
		for i, li := range layerBlobs {
			s2LayerInfos[i] = types.BlobInfo{
				Digest:    li.Digest,
				Size:      li.Size,
				MediaType: s2LayerMediaType,
			}
		}
		s2ManifestBytes := buildManifest(configInfo, s2LayerInfos, s2ManifestMT, s2LayerMediaType, s2ConfigMediaType)
		named, err := dockerref.ParseNormalizedNamed("diffah-fixture:" + vs.version)
		if err != nil {
			return fmt.Errorf("parse docker ref %s: %w", vs.name, err)
		}
		nt, ok := named.(dockerref.NamedTagged)
		if !ok {
			return fmt.Errorf("docker ref not NamedTagged: %s", vs.name)
		}
		s2Ref, err := dockerarchive.NewReference(s2Path, nt)
		if err != nil {
			return fmt.Errorf("docker archive ref %s: %w", vs.name, err)
		}
		if err := writeFixture(ctx, s2Ref, layers, s2LayerInfos, configJSON, s2ManifestBytes); err != nil {
			return fmt.Errorf("write s2 fixture %s: %w", vs.name, err)
		}
		if err := normalizeTar(s2Path); err != nil {
			return fmt.Errorf("normalize s2 tar %s: %w", vs.name, err)
		}
		fmt.Printf("wrote %s\n", s2Path)

		// Collect checksums in order: oci then s2 for this version.
		for _, fp := range []string{ociPath, s2Path} {
			cksum, err := fileChecksum(fp)
			if err != nil {
				return fmt.Errorf("checksum %s: %w", fp, err)
			}
			outputs = append(outputs, archiveOutput{
				filename: filepath.Base(fp),
				checksum: cksum,
			})
		}
	}

	// Build unrelated OCI fixture: 1 layer with /unrelated.bin = 16 KiB of 0xFF.
	// Its layer digest must not overlap with any v1/v2 layer.
	unrelatedData := bytes.Repeat([]byte{0xFF}, 16*1024)
	unrelatedCompressed, unrelatedDiffID, unrelatedBlob := buildLayerBlob("unrelated.bin", unrelatedData)
	fmt.Printf("unrelated layer diffID: %s\n", unrelatedDiffID)
	fmt.Printf("unrelated layer blobDigest: %s\n", unrelatedBlob)

	unrelatedLayerInfos := []types.BlobInfo{
		{Digest: unrelatedBlob, Size: int64(len(unrelatedCompressed)), MediaType: ociLayerMediaType},
	}
	unrelatedConfigJSON := buildConfig([]digest.Digest{unrelatedDiffID})
	h := sha256.New()
	h.Write(unrelatedConfigJSON)
	unrelatedConfigDigest := digest.NewDigestFromEncoded(digest.SHA256, fmt.Sprintf("%x", h.Sum(nil)))
	unrelatedConfigInfo := types.BlobInfo{
		Digest: unrelatedConfigDigest,
		Size:   int64(len(unrelatedConfigJSON)),
	}
	unrelatedManifestBytes := buildManifest(
		unrelatedConfigInfo, unrelatedLayerInfos,
		ociManifestMT, ociLayerMediaType, ociConfigMediaType,
	)
	unrelatedPath := filepath.Join(fixtureDir, "unrelated_oci.tar")
	unrelatedRef, err := ociarchive.NewReference(unrelatedPath, "diffah-fixture-unrelated:v1")
	if err != nil {
		return fmt.Errorf("unrelated oci ref: %w", err)
	}
	if err := writeFixture(
		ctx, unrelatedRef, [][]byte{unrelatedCompressed},
		unrelatedLayerInfos, unrelatedConfigJSON, unrelatedManifestBytes,
	); err != nil {
		return fmt.Errorf("write unrelated oci fixture: %w", err)
	}
	if err := normalizeTar(unrelatedPath); err != nil {
		return fmt.Errorf("normalize unrelated oci tar: %w", err)
	}
	fmt.Printf("wrote %s\n", unrelatedPath)

	unrelatedCksum, err := fileChecksum(unrelatedPath)
	if err != nil {
		return fmt.Errorf("checksum unrelated_oci.tar: %w", err)
	}
	outputs = append(outputs, archiveOutput{
		filename: filepath.Base(unrelatedPath),
		checksum: unrelatedCksum,
	})

	// Write CHECKSUMS.
	checksumPath := filepath.Join(fixtureDir, "CHECKSUMS")
	cf, err := os.Create(checksumPath)
	if err != nil {
		return fmt.Errorf("create CHECKSUMS: %w", err)
	}
	defer cf.Close()
	for _, o := range outputs {
		fmt.Fprintf(cf, "%s  %s\n", o.checksum, o.filename)
	}
	fmt.Printf("wrote %s\n", checksumPath)
	return nil
}

func main() {
	ctx := context.Background()
	if err := buildFixtures(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
