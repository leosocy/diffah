package zstdpatch

import "log/slog"

func log() *slog.Logger {
	return slog.Default().With("component", "zstdpatch")
}
