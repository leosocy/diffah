package exporter

import (
	"context"
	"fmt"
	"io"
	"time"
)

type Options struct {
	Pairs       []Pair
	Platform    string
	Compress    string
	OutputPath  string
	ToolVersion string
	IntraLayer  string
	CreatedAt   time.Time
	Progress    io.Writer

	fingerprinter Fingerprinter
}

func Export(ctx context.Context, opts Options) error {
	return fmt.Errorf("bundle export not yet wired in this commit")
}

type DryRunStats struct {
	ShippedCount  int
	ShippedBytes  int64
	RequiredCount int
	RequiredBytes int64
}

func DryRun(ctx context.Context, opts Options) (DryRunStats, error) {
	return DryRunStats{}, fmt.Errorf("bundle dry-run not yet wired in this commit")
}
