package progress_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/leosocy/diffah/pkg/progress"
)

func TestDiscard_Printf(_ *testing.T) {
	r := progress.Discard
	r.Printf("this writes nowhere %d", 42)
}

func TestLine_Printf(t *testing.T) {
	var buf bytes.Buffer
	r := progress.Line(&buf)
	r.Printf("planning %d pairs", 3)

	got := buf.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("Line.Printf output missing trailing newline: %q", got)
	}
	if !strings.Contains(got, "planning 3 pairs") {
		t.Errorf("Line.Printf output = %q, want substring %q", got, "planning 3 pairs")
	}
}

func TestLine_Printf_MultipleMessages(t *testing.T) {
	var buf bytes.Buffer
	r := progress.Line(&buf)
	r.Printf("step one")
	r.Printf("step two")

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), buf.String())
	}
	if lines[0] != "step one" {
		t.Errorf("line 0 = %q, want %q", lines[0], "step one")
	}
	if lines[1] != "step two" {
		t.Errorf("line 1 = %q, want %q", lines[1], "step two")
	}
}

func TestFromWriter_Nil(t *testing.T) {
	r := progress.FromWriter(nil)
	if r != progress.Discard {
		t.Errorf("FromWriter(nil) = %T, want Discard", r)
	}
}

func TestFromWriter_NonNil(t *testing.T) {
	var buf bytes.Buffer
	r := progress.FromWriter(&buf)
	r.Printf("hello %s", "world")

	if buf.String() != "hello world\n" {
		t.Errorf("FromWriter output = %q, want %q", buf.String(), "hello world\n")
	}
}

func TestReporter_Interface(_ *testing.T) {
	implementations := []progress.Reporter{
		progress.Discard,
		progress.Line(&bytes.Buffer{}),
		progress.FromWriter(&bytes.Buffer{}),
		progress.FromWriter(nil),
	}
	for _, r := range implementations {
		r.Printf("interface check %d", 1)
	}
}
