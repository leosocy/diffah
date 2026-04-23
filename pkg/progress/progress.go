package progress

import (
	"fmt"
	"io"
)

type Reporter interface {
	Printf(format string, args ...any)
}

type discardReporter struct{}

func (discardReporter) Printf(string, ...any) {}

var Discard Reporter = discardReporter{}

type lineReporter struct {
	w io.Writer
}

func (l lineReporter) Printf(format string, args ...any) {
	fmt.Fprintf(l.w, format+"\n", args...)
}

func Line(w io.Writer) Reporter {
	return lineReporter{w: w}
}

func FromWriter(w io.Writer) Reporter {
	if w == nil {
		return Discard
	}
	return Line(w)
}
