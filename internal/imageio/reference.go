// Package imageio adapts containers-image transports for the diffah service
// layer. It hides the upstream API surface so service code can stay focused
// on the diff and reconstruction algorithms.
package imageio

import (
	"fmt"
	"strings"

	"go.podman.io/image/v5/transports/alltransports"
	"go.podman.io/image/v5/types"
)

// ParseReference parses a "transport:reference" string and returns the
// corresponding types.ImageReference. The error wraps the input value so
// callers can surface what was rejected.
func ParseReference(ref string) (types.ImageReference, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("empty image reference")
	}
	r, err := alltransports.ParseImageName(ref)
	if err != nil {
		return nil, fmt.Errorf("parse reference %q: %w", ref, err)
	}
	return r, nil
}
