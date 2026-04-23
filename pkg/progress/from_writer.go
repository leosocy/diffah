package progress

import "io"

func FromWriter(w io.Writer) Reporter {
	if w == nil {
		return NewDiscard()
	}
	return NewLine(w)
}
