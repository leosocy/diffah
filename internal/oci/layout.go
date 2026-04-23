package oci

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ReadDirManifest reads <dir>/manifest.json and returns its raw bytes along
// with the manifest's declared media type.
//
// It does not call into containers-image so it can be used during archive
// packing without opening an ImageSource.
func ReadDirManifest(dir string) ([]byte, string, error) {
	path := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", path, err)
	}
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, "", fmt.Errorf("decode manifest %s: %w", path, err)
	}
	if probe.MediaType == "" {
		return nil, "", fmt.Errorf("manifest %s has empty mediaType", path)
	}
	log().Debug("read dir manifest", "path", path, "media_type", probe.MediaType)
	return raw, probe.MediaType, nil
}
