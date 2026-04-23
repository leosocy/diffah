package imageio

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	dockerarchive "go.podman.io/image/v5/docker/archive"
	dockerref "go.podman.io/image/v5/docker/reference"
	ociarchive "go.podman.io/image/v5/oci/archive"
	"go.podman.io/image/v5/types"
)

const (
	FormatOCIArchive    = "oci-archive"
	FormatDockerArchive = "docker-archive"
)

func SniffArchiveFormat(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for i := 0; i < 64; i++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar %s: %w", path, err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == "oci-layout" {
			log().Debug("sniffed archive format", "path", path, "format", FormatOCIArchive)
			return FormatOCIArchive, nil
		}
		if name == "manifest.json" {
			log().Debug("sniffed archive format", "path", path, "format", FormatDockerArchive)
			return FormatDockerArchive, nil
		}
	}
	return "", fmt.Errorf("cannot determine archive format for %s", path)
}

func OpenArchiveRef(path string) (types.ImageReference, error) {
	format, err := SniffArchiveFormat(path)
	if err != nil {
		return nil, err
	}
	switch format {
	case FormatOCIArchive:
		return ociarchive.NewReference(path, "")
	case FormatDockerArchive:
		named, err := dockerref.ParseNormalizedNamed("diffah-in:latest")
		if err != nil {
			return nil, fmt.Errorf("build docker ref: %w", err)
		}
		nt, ok := named.(dockerref.NamedTagged)
		if !ok {
			return nil, fmt.Errorf("docker ref not NamedTagged")
		}
		return dockerarchive.NewReference(path, nt)
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}
