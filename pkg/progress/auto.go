package progress

import (
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

func NewAuto(w io.Writer) Reporter {
	if w == nil {
		return NewDiscard()
	}
	return newAutoFor(w, IsTTY(w), !noColor(), os.Getenv("CI") == "true")
}

func newAutoFor(w io.Writer, tty, color, ci bool) Reporter {
	if tty && !ci && color {
		return NewBars(w)
	}
	return NewLine(w)
}

// IsTTY reports whether w is an *os.File pointing at a terminal or cygwin pty.
func IsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func noColor() bool { return os.Getenv("NO_COLOR") != "" }
