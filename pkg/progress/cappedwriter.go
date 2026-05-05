package progress

import "io"

// CappedWriter returns an onChunk callback that forwards up to total
// bytes to sink, clamping chunks that would cross the cap and dropping
// anything after. Used to keep per-blob progress bars from overshooting
// the manifest-declared size when transports emit decompressed bytes
// (e.g., oci-archive).
//
// CappedWriter and CountingReader are exposed here so both exporter
// (encode pipeline) and importer (serveFull/servePatch streaming
// readers) report per-blob bytes through the same primitives without
// either side depending on the other.
func CappedWriter(total int64, sink func(int64)) func(int64) {
	remaining := total
	return func(n int64) {
		if remaining <= 0 {
			return
		}
		if n > remaining {
			n = remaining
		}
		sink(n)
		remaining -= n
	}
}

// CountingReader wraps r, reporting each successful Read's byte count to
// OnChunk. The wrapped reader's Close (if any) is propagated by the
// caller — CountingReader is io.Reader-only.
type CountingReader struct {
	R       io.Reader
	OnChunk func(int64)
}

// Read implements io.Reader. n is reported to OnChunk only when n > 0
// and OnChunk is non-nil; the underlying error (including io.EOF) is
// returned unchanged so the caller can drive normal stream termination.
func (r *CountingReader) Read(p []byte) (int, error) {
	n, err := r.R.Read(p)
	if n > 0 && r.OnChunk != nil {
		r.OnChunk(int64(n))
	}
	return n, err
}
