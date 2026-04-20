package importer

import (
	"fmt"

	"github.com/leosocy/diffah/pkg/diff"
)

// Known single-image manifest media types. Manifest lists are rejected
// upstream by the exporter, so these two cover every sidecar we can see.
const (
	mimeDockerSchema2 = "application/vnd.docker.distribution.manifest.v2+json"
	mimeOCIManifest   = "application/vnd.oci.image.manifest.v1+json"
)

// resolveOutputFormat validates the user-supplied --output-format against
// the source manifest media type recorded in the sidecar. The zero value
// "" means "auto": pick the format that preserves the source bytes. An
// explicit format that would force media-type conversion is rejected with
// diff.ErrIncompatibleOutputFormat unless allowConvert is set.
//
// dir output is always permitted because the dir transport copies blobs
// byte-for-byte regardless of the declared manifest media type.
func resolveOutputFormat(userFmt, sourceMime string, allowConvert bool) (string, error) {
	preferred, err := defaultFormatForMime(sourceMime)
	if err != nil && userFmt == "" {
		return "", err
	}

	switch userFmt {
	case "":
		return preferred, nil
	case FormatDir:
		return FormatDir, nil
	case FormatDockerArchive, FormatOCIArchive:
		if userFmt == preferred || allowConvert {
			return userFmt, nil
		}
		return "", &diff.ErrIncompatibleOutputFormat{
			SourceMime:   sourceMime,
			OutputFormat: userFmt,
		}
	default:
		return "", fmt.Errorf("unknown --output-format %q", userFmt)
	}
}

// defaultFormatForMime maps a sidecar manifest media type to the output
// format that preserves its bytes.
func defaultFormatForMime(mime string) (string, error) {
	switch mime {
	case mimeDockerSchema2:
		return FormatDockerArchive, nil
	case mimeOCIManifest:
		return FormatOCIArchive, nil
	default:
		return "", fmt.Errorf("sidecar target media type %q has no default output format", mime)
	}
}
