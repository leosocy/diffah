package exporter

import (
	"path/filepath"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

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
				SourceHint:     filepath.Base(p.BaselineRef),
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
