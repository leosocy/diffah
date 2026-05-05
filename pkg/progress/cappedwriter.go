package progress

// CappedWriter returns an onChunk callback that forwards up to total
// bytes to sink, clamping chunks that would cross the cap and dropping
// anything after. Used to keep per-blob progress bars from overshooting
// the manifest-declared size when transports emit decompressed bytes
// (e.g., oci-archive).
//
// Lifted from pkg/exporter so the importer can reuse the same clamp
// without either side depending on the other.
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
