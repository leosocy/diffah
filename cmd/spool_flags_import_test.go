package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestImportSpoolFlags_DefaultsAndParse(t *testing.T) {
	cmd := &cobra.Command{Use: "fake"}
	build := installImportSpoolFlags(cmd)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	got, err := build()
	if err != nil {
		t.Fatalf("build defaults: %v", err)
	}
	if got.Workdir != "" || got.MemoryBudget != 8<<30 || got.Workers != 8 {
		t.Fatalf("default mismatch: %+v", got)
	}

	cmd = &cobra.Command{Use: "fake"}
	build = installImportSpoolFlags(cmd)
	if err := cmd.ParseFlags([]string{"--workdir=/tmp/x", "--memory-budget=512MiB", "--workers=2"}); err != nil {
		t.Fatalf("parse explicit: %v", err)
	}
	got, err = build()
	if err != nil {
		t.Fatalf("build explicit: %v", err)
	}
	if got.Workdir != "/tmp/x" || got.MemoryBudget != 512<<20 || got.Workers != 2 {
		t.Fatalf("explicit mismatch: %+v", got)
	}

	cmd = &cobra.Command{Use: "fake"}
	build = installImportSpoolFlags(cmd)
	if err := cmd.ParseFlags([]string{"--memory-budget=not-a-number"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := build(); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("expected parse error, got %v", err)
	}
}
