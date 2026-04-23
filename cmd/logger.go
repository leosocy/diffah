package cmd

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
)

func pickHandler(w io.Writer, format string, opts *slog.HandlerOptions, tty bool) slog.Handler {
	switch strings.ToLower(format) {
	case "json":
		return slog.NewJSONHandler(w, opts)
	case "text":
		return slog.NewTextHandler(w, opts)
	case "", "auto":
		if tty && os.Getenv("CI") != "true" {
			return slog.NewTextHandler(w, opts)
		}
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewJSONHandler(w, opts)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "info", "":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func installLogger(stderr io.Writer, levelFlag, formatFlag string, quiet, verbose bool) *slog.Logger {
	level := parseLevel(levelFlag)
	if verbose {
		level = slog.LevelDebug
	}
	if quiet {
		level = slog.LevelWarn
	}
	opts := &slog.HandlerOptions{Level: level}
	tty := isTTY(stderr)
	h := pickHandler(stderr, formatFlag, opts, tty)
	logger := slog.New(h)
	slog.SetDefault(logger)
	return logger
}
