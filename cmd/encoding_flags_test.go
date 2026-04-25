package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestEncodingFlags_Defaults(t *testing.T) {
	c := &cobra.Command{Use: "x"}
	build := installEncodingFlags(c)
	if err := c.ParseFlags(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got.Workers != 1 || got.Candidates != 1 || got.ZstdLevel != 3 || got.ZstdWindowLog != 27 {
		t.Fatalf("unexpected defaults: %+v", got)
	}
}

func TestEncodingFlags_Auto(t *testing.T) {
	c := &cobra.Command{Use: "x"}
	build := installEncodingFlags(c)
	if err := c.ParseFlags([]string{"--zstd-window-log=auto"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got.ZstdWindowLog != 0 {
		t.Fatalf("auto should yield ZstdWindowLog=0 (sentinel), got %d", got.ZstdWindowLog)
	}
}

func TestEncodingFlags_Validation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string // substring expected in error message
	}{
		{"workers zero", []string{"--workers=0"}, "--workers must be >= 1"},
		{"candidates zero", []string{"--candidates=0"}, "--candidates must be >= 1"},
		{"level zero", []string{"--zstd-level=0"}, "--zstd-level must be in [1,22]"},
		{"level high", []string{"--zstd-level=23"}, "--zstd-level must be in [1,22]"},
		{"window log low", []string{"--zstd-window-log=9"}, "--zstd-window-log must be 'auto' or in [10,31]"},
		{"window log high", []string{"--zstd-window-log=32"}, "--zstd-window-log must be 'auto' or in [10,31]"},
		{"window log non-numeric", []string{"--zstd-window-log=foo"}, "--zstd-window-log must be 'auto' or in [10,31]"},
		{"window log trailing garbage", []string{"--zstd-window-log=27foo"}, "--zstd-window-log must be 'auto' or in [10,31]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &cobra.Command{Use: "x"}
			build := installEncodingFlags(c)
			if err := c.ParseFlags(tc.args); err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, err := build()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}
