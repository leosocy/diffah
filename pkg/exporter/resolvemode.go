package exporter

import (
	"context"
	"fmt"
	"io"

	"github.com/leosocy/diffah/internal/zstdpatch"
)

const (
	modeAuto     = "auto"
	modeOff      = "off"
	modeRequired = "required"
)

type Probe func(context.Context) (ok bool, reason string)

func resolveMode(
	ctx context.Context, userMode string, probe Probe, warn io.Writer,
) (effective string, err error) {
	if userMode == "" {
		userMode = modeAuto
	}
	switch userMode {
	case modeAuto:
		ok, reason := probe(ctx)
		if ok {
			return modeAuto, nil
		}
		if warn != nil {
			fmt.Fprintf(warn, "diffah: %s; disabling intra-layer for this run\n", reason)
		}
		return modeOff, nil
	case modeOff:
		return modeOff, nil
	case modeRequired:
		ok, reason := probe(ctx)
		if ok {
			return modeAuto, nil
		}
		return "", fmt.Errorf("%w: %s", zstdpatch.ErrZstdBinaryMissing, reason)
	default:
		return "", fmt.Errorf(
			"--intra-layer=%q not recognized; valid values: auto, off, required",
			userMode)
	}
}
