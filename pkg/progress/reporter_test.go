package progress_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/progress"
)

func TestDiscard_Nop(_ *testing.T) {
	r := progress.NewDiscard()
	r.Phase("encoding")
	l := r.StartLayer(digest.Digest("sha256:abc"), 100, "full")
	l.Written(50)
	l.Done()
	r.Finish()
}

func TestDiscard_LayerFail(_ *testing.T) {
	r := progress.NewDiscard()
	l := r.StartLayer(digest.Digest("sha256:abc"), 100, "patch")
	l.Fail(bytesErr("boom"))
	r.Finish()
}

func TestLine_EmitsPhaseAndDone(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewLine(&buf)
	r.Phase("encoding")
	l := r.StartLayer(digest.Digest("sha256:abcdefghijklmnop"), 1024, "full")
	l.Written(1024)
	l.Done()
	r.Finish()

	out := buf.String()
	if !strings.Contains(out, "[encoding]") {
		t.Errorf("expected [encoding] phase line, got %q", out)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("expected 'done' line, got %q", out)
	}
	if !strings.Contains(out, "sha256:abcdefghijkl") {
		t.Errorf("expected layer digest prefix, got %q", out)
	}
}

func TestLine_ReportsFail(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewLine(&buf)
	l := r.StartLayer(digest.Digest("sha256:abc"), 100, "patch")
	l.Fail(bytesErr("encode failed"))
	if !strings.Contains(buf.String(), "failed") {
		t.Errorf("expected 'failed' in line output, got %q", buf.String())
	}
}

func TestLine_WrittenAccumulates(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewLine(&buf)
	l := r.StartLayer(digest.Digest("sha256:aaa"), 2048, "full")
	l.Written(512)
	l.Written(512)
	l.Done()

	out := buf.String()
	if !strings.Contains(out, "1.0KB") {
		t.Errorf("expected accumulated byte count '1.0KB', got %q", out)
	}
}

func TestLine_DoneIdempotent(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewLine(&buf)
	l := r.StartLayer(digest.Digest("sha256:aaa"), 100, "full")
	l.Done()
	l.Done()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Errorf("expected exactly 1 done line, got %d: %q", len(lines), buf.String())
	}
}

func TestLine_FailAfterDoneIsNop(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewLine(&buf)
	l := r.StartLayer(digest.Digest("sha256:aaa"), 100, "full")
	l.Done()
	l.Fail(bytesErr("should not appear"))
	if strings.Contains(buf.String(), "failed") {
		t.Errorf("Fail after Done should be a no-op, got %q", buf.String())
	}
}

func TestLine_DoneAfterFailIsNop(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewLine(&buf)
	l := r.StartLayer(digest.Digest("sha256:aaa"), 100, "full")
	l.Fail(bytesErr("error"))
	l.Done()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Errorf("Done after Fail should be a no-op, got %d lines: %q", len(lines), buf.String())
	}
}

func TestLine_WrittenAfterDoneIsNop(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewLine(&buf)
	l := r.StartLayer(digest.Digest("sha256:aaa"), 2048, "full")
	l.Written(512)
	l.Done()
	l.Written(512)
	if strings.Contains(buf.String(), "1.0KB") {
		t.Errorf("Written after Done should not accumulate, got %q", buf.String())
	}
}

func TestLine_MultiplePhases(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewLine(&buf)
	r.Phase("planning")
	r.Phase("encoding")
	r.Phase("writing")

	out := buf.String()
	if !strings.Contains(out, "[planning]") {
		t.Errorf("expected [planning], got %q", out)
	}
	if !strings.Contains(out, "[encoding]") {
		t.Errorf("expected [encoding], got %q", out)
	}
	if !strings.Contains(out, "[writing]") {
		t.Errorf("expected [writing], got %q", out)
	}
}

func TestFromWriter_Nil(_ *testing.T) {
	r := progress.FromWriter(nil)
	r.Phase("planning")
	l := r.StartLayer(digest.Digest("sha256:abc"), 100, "full")
	l.Done()
	r.Finish()
}

func TestFromWriter_NonNil(t *testing.T) {
	var buf bytes.Buffer
	r := progress.FromWriter(&buf)
	r.Phase("planning")
	if !strings.Contains(buf.String(), "[planning]") {
		t.Errorf("expected phase line, got %q", buf.String())
	}
}

func TestNewBars_FallsBackToLineOnNonTTY(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewBars(&buf)
	r.Phase("encoding")
	l := r.StartLayer(digest.Digest("sha256:fallbackcafebabe"), 1024, "full")
	l.Written(1024)
	l.Done()
	r.Finish()

	out := buf.String()
	if !strings.Contains(out, "[encoding]") {
		t.Errorf("non-TTY NewBars must still emit phase markers (line fallback), got %q", out)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("non-TTY NewBars must still emit layer done lines (line fallback), got %q", out)
	}
}

func TestReporter_Interface(_ *testing.T) {
	implementations := []progress.Reporter{
		progress.NewDiscard(),
		progress.NewLine(&bytes.Buffer{}),
		progress.NewBars(&bytes.Buffer{}),
		progress.NewAuto(&bytes.Buffer{}),
		progress.NewAuto(nil),
		progress.FromWriter(&bytes.Buffer{}),
		progress.FromWriter(nil),
	}
	for _, r := range implementations {
		r.Phase("test")
		l := r.StartLayer(digest.Digest("sha256:abc"), 100, "full")
		l.Written(50)
		l.Done()
		r.Finish()
	}
}

type bytesErr string

func (e bytesErr) Error() string { return string(e) }
