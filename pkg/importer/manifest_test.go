package importer

import (
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestParseManifestLayers_OCI(t *testing.T) {
	manifestBytes := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},
		"layers":[
			{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:l1","size":100},
			{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:l2","size":200}
		]
	}`)
	layers, mediaType, err := parseManifestLayers(manifestBytes,
		"application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		t.Fatal(err)
	}
	if mediaType != "application/vnd.oci.image.manifest.v1+json" {
		t.Errorf("mediaType = %q", mediaType)
	}
	if len(layers) != 2 {
		t.Fatalf("len(layers) = %d, want 2", len(layers))
	}
	if layers[0].Digest != digest.Digest("sha256:l1") {
		t.Errorf("layers[0].Digest = %v", layers[0].Digest)
	}
	if layers[0].Size != 100 {
		t.Errorf("layers[0].Size = %d", layers[0].Size)
	}
	if layers[1].Digest != digest.Digest("sha256:l2") {
		t.Errorf("layers[1].Digest = %v", layers[1].Digest)
	}
	if layers[1].Size != 200 {
		t.Errorf("layers[1].Size = %d", layers[1].Size)
	}
}

func TestParseManifestLayers_DockerSchema2(t *testing.T) {
	manifestBytes := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
		"config": {"mediaType":"application/vnd.docker.container.image.v1+json","digest":"sha256:cfg","size":10},
		"layers":[
			{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":"sha256:l1","size":100}
		]
	}`)
	layers, _, err := parseManifestLayers(manifestBytes,
		"application/vnd.docker.distribution.manifest.v2+json")
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 || layers[0].Digest != digest.Digest("sha256:l1") {
		t.Fatalf("layers = %v", layers)
	}
}

func TestParseManifestLayers_UnsupportedMediaType(t *testing.T) {
	_, _, err := parseManifestLayers([]byte(`{}`),
		"application/vnd.oci.image.index.v1+json")
	if err == nil {
		t.Fatal("expected error for manifest list / index media type, got nil")
	}
}
