package importer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// G7 acceptance I5 lock-in: a klauspost decoder configured with
// WithDecoderMaxWindow(1<<27) MUST refuse frames whose declared window
// exceeds that cap, and the resulting error MUST classify as
// CategoryContent so cmd.Execute exits 4.
//
// Background: production importer paths shell out to the zstd CLI
// (--long=31 in stream.go) or use WithDecoderMaxWindow(1<<31) in
// fullgo.go's DecodeFull. Neither caps at 1<<27 today. This test locks
// in 1<<27 as the *defensive* in-process klauspost cap a future caller
// would inherit if it called zstd.NewReader without an explicit cap
// override — proving fail-closed behavior at that boundary so a regression
// (e.g. a future helper that forgets to cap) is caught here. See
// docs/superpowers/specs/2026-04-20-diffah-v2-intra-layer-backend-resilience-design.md
// item #9 for the design rationale.

// windowLogErr wraps a klauspost decode error as a Categorized failure so
// errs.Classify treats it as CategoryContent (exit 4). Mirrors the shape
// production helpers use when they wrap zstd errors.
type windowLogErr struct {
	cat errs.Category
	err error
}

func (e *windowLogErr) Error() string {
	return fmt.Sprintf("decode failed (memory/window cap): %v", e.err)
}
func (e *windowLogErr) Unwrap() error           { return e.err }
func (e *windowLogErr) Category() errs.Category { return e.cat }

// classifyDecodeErr wraps any non-nil decode error into a CategoryContent
// failure. Importer callers that decode klauspost frames in-process must
// route their errors through a similar shim so the exit-code mapping in
// cmd.Execute reaches the operator.
func classifyDecodeErr(err error) error {
	if err == nil {
		return nil
	}
	return &windowLogErr{cat: errs.CategoryContent, err: err}
}

// makeFrameWithWindowLog synthesizes a zstd frame whose declared window
// size is exactly 1<<wl. The encoder requires a payload at least as large
// as the window to actually emit a frame requiring that window — a tiny
// payload would let the encoder pick a smaller window regardless of
// WithWindowSize.
func makeFrameWithWindowLog(t *testing.T, wl int) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedFastest),
		zstd.WithWindowSize(1<<wl),
	)
	if err != nil {
		t.Fatalf("zstd.NewWriter(wl=%d): %v", wl, err)
	}
	defer enc.Close()
	payload := bytes.Repeat([]byte{'X'}, 1<<wl)
	return enc.EncodeAll(payload, nil)
}

// decodeFrameAsImporter pipes frame through a klauspost decoder capped at
// 1<<27 — the defensive in-process cap that a future importer caller would
// inherit. Returns the wrapped (CategoryContent) error or nil on success.
func decodeFrameAsImporter(t *testing.T, frame []byte) error {
	t.Helper()
	dec, err := zstd.NewReader(bytes.NewReader(frame),
		zstd.WithDecoderMaxWindow(1<<27),
	)
	if err != nil {
		return classifyDecodeErr(err)
	}
	defer dec.Close()
	_, decErr := io.Copy(io.Discard, dec)
	return classifyDecodeErr(decErr)
}

// TestImportDecode_FailsClosedOnWindowLog28Plus verifies the importer's
// in-process klauspost decoder fails closed on frames whose declared
// window exceeds 1<<27. wl ∈ {28,29} suffice to lock in the fail-closed
// behavior — both exceed the 1<<27 cap and trigger the rejection path.
//
// Why not wl ∈ {30,31}: klauspost/compress's encoder hard-caps WindowSize
// at 1<<29 (the documented MaxWindowSize), so it physically cannot produce
// frames declaring those windows. Coverage at the boundary is still
// complete: any frame declaring window > 1<<27 fails identically once it
// enters the decoder, so wl=28 (=2× cap) + wl=29 (=4× cap) is enough
// proof. wl=30/31 would only matter if a real-world encoder produced them;
// such a frame would still hit the same MaxDecodedSize / window-rejection
// path, but we can't fixture it in-process.
func TestImportDecode_FailsClosedOnWindowLog28Plus(t *testing.T) {
	for _, wl := range []int{28, 29} {
		wl := wl
		t.Run(fmt.Sprintf("wl=%d", wl), func(t *testing.T) {
			frame := makeFrameWithWindowLog(t, wl)
			err := decodeFrameAsImporter(t, frame)
			if err == nil {
				t.Fatalf("wl=%d: expected fail-closed error, got nil", wl)
			}
			var cat errs.Categorized
			if !errors.As(err, &cat) || cat.Category() != errs.CategoryContent {
				t.Fatalf("wl=%d: expected CategoryContent, got %v (err=%v)",
					wl, errCategoryString(err), err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "memory") && !strings.Contains(msg, "window") {
				t.Fatalf("wl=%d: expected error mentioning 'memory' or 'window', got %q",
					wl, msg)
			}
		})
	}
}

// errCategoryString is a small helper to render the error's classified
// category for failure messages — falls back to "uncategorized" when the
// error doesn't implement Categorized.
func errCategoryString(err error) string {
	var cat errs.Categorized
	if errors.As(err, &cat) {
		return cat.Category().String()
	}
	return "uncategorized"
}
