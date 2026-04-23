package progress

import (
	"bytes"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestNewAuto_NonTTY_PicksLine(t *testing.T) {
	var buf bytes.Buffer
	r := newAutoFor(&buf, false, true, false)
	if _, ok := r.(*lineReporter); !ok {
		t.Errorf("non-TTY expected *lineReporter, got %T", r)
	}
	r.Phase("test")
	out := buf.String()
	if !contains(out, "[test]") {
		t.Errorf("non-TTY NewAuto expected line output, got %q", out)
	}
	if containsEscape(out) {
		t.Errorf("non-TTY output must not contain escape sequences, got %q", out)
	}
}

func TestNewAuto_CI_PicksLine(t *testing.T) {
	var buf bytes.Buffer
	r := newAutoFor(&buf, true, true, true)
	r.Phase("test")
	if containsEscape(buf.String()) {
		t.Errorf("CI=true must degrade to lineReporter, got escape sequences")
	}
}

func TestNewAuto_NoColor_PicksLine(t *testing.T) {
	var buf bytes.Buffer
	r := newAutoFor(&buf, true, false, false)
	r.Phase("test")
	if containsEscape(buf.String()) {
		t.Errorf("NO_COLOR must degrade to lineReporter, got escape sequences")
	}
}

func TestNewAuto_TTY_PicksBars(t *testing.T) {
	var buf bytes.Buffer
	r := newAutoFor(&buf, true, true, false)
	if _, ok := r.(*barsReporter); !ok {
		t.Errorf("TTY+color+!CI expected *barsReporter, got %T", r)
	}
	r.Phase("test")
	l := r.StartLayer(digest.Digest("sha256:aaa"), 1024, "full")
	l.Written(512)
	l.Done()
	r.Finish()
}

func TestNewAuto_Nil_PicksDiscard(_ *testing.T) {
	r := NewAuto(nil)
	r.Phase("test")
	l := r.StartLayer(digest.Digest("sha256:abc"), 100, "full")
	l.Written(50)
	l.Done()
	r.Finish()
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func containsEscape(s string) bool {
	for _, r := range s {
		if r == 0x1b {
			return true
		}
	}
	return false
}
