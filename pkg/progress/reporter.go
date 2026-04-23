package progress

import (
	"io"

	"github.com/opencontainers/go-digest"
)

type Reporter interface {
	Phase(name string)
	StartLayer(d digest.Digest, totalBytes int64, encoding string) Layer
	Finish()
}

type Layer interface {
	Written(n int64)
	Done()
	Fail(err error)
}

type SlogWriterProvider interface {
	SlogWriter() io.Writer
}
