package exporter

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

// sourceHintFor derives a compact provenance string from a transport
// reference. For archive transports it returns the file basename; for
// registry transports it returns the canonical repo:tag form. The
// result is informational — it shows up in the sidecar so consumers can
// tell where each blob "came from" — so registry refs should land as
// "host/repo:tag" rather than filepath.Base's unhelpful "tag".
func sourceHintFor(ref string) string {
	for _, prefix := range []string{"docker-archive:", "oci-archive:", "oci:", "dir:"} {
		if rest, ok := strings.CutPrefix(ref, prefix); ok {
			return filepath.Base(rest)
		}
	}
	if rest, ok := strings.CutPrefix(ref, "docker://"); ok {
		return rest
	}
	return ref
}

func assembleSidecar(
	pool *blobPool, pairs []*pairPlan, platform string, toolVersion string, createdAt time.Time,
) diff.Sidecar {
	s := diff.Sidecar{
		Version:     diff.SchemaVersionV1,
		Feature:     diff.FeatureBundle,
		Tool:        "diffah",
		ToolVersion: toolVersion,
		CreatedAt:   createdAt,
		Platform:    platform,
		Blobs:       make(map[digest.Digest]diff.BlobEntry, len(pool.entries)),
		Images:      make([]diff.ImageEntry, 0, len(pairs)),
	}
	for d, e := range pool.entries {
		s.Blobs[d] = e
	}
	for _, p := range pairs {
		mfDigest := digest.FromBytes(p.TargetManifest)
		s.Images = append(s.Images, diff.ImageEntry{
			Name: p.Name,
			Baseline: diff.BaselineRef{
				ManifestDigest: digest.FromBytes(p.BaselineManifest),
				MediaType:      p.BaselineMediaType,
				SourceHint:     sourceHintFor(p.BaselineRef),
			},
			Target: diff.TargetRef{
				ManifestDigest: mfDigest,
				ManifestSize:   int64(len(p.TargetManifest)),
				MediaType:      p.TargetMediaType,
			},
		})
	}
	return s
}
