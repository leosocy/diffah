package oci

import "log/slog"

func log() *slog.Logger {
	return slog.Default().With("component", "oci")
}
