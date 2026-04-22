package exporter

import (
	"context"
	"fmt"
	"io"

	"github.com/leosocy/diffah/internal/zstdpatch"
)

type Probe func(context.Context) (ok bool, reason string)

func resolveMode(
	ctx context.Context, userMode string, probe Probe, warn io.Writer,
) (effective string, err error) {
	if userMode == "" {
		userMode = "auto"
	}
	switch userMode {
	case "auto":
		ok, reason := probe(ctx)
		if ok {
			return "auto", nil
		}
		if warn != nil {
			fmt.Fprintf(warn, "diffah: %s; disabling intra-layer for this run\n", reason)
		}
		return "off", nil
	case "off":
		return "off", nil
	case "required":
		ok, reason := probe(ctx)
		if ok {
			return "auto", nil
		}
		return "", fmt.Errorf("%w: %s", zstdpatch.ErrZstdBinaryMissing, reason)
	default:
		return "", fmt.Errorf(
			"--intra-layer=%q not recognized; valid values: auto, off, required",
			userMode)
	}
}
