package progress

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/opencontainers/go-digest"
)

func NewLine(w io.Writer) Reporter { return &lineReporter{w: w} }

type lineReporter struct {
	w  io.Writer
	mu sync.Mutex
}

func (r *lineReporter) Phase(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, "[%s]\n", name)
}

func (r *lineReporter) StartLayer(d digest.Digest, total int64, enc string) Layer {
	return &lineLayer{r: r, digest: d, total: total, enc: enc, started: time.Now()}
}

func (r *lineReporter) Finish() {}

type lineLayer struct {
	r       *lineReporter
	digest  digest.Digest
	total   int64
	enc     string
	written int64
	started time.Time
	done    bool
	mu      sync.Mutex
}

func (l *lineLayer) Written(n int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return
	}
	l.written += n
}

func (l *lineLayer) Done() {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	written := l.written
	dur := time.Since(l.started)
	l.mu.Unlock()

	l.r.mu.Lock()
	defer l.r.mu.Unlock()
	fmt.Fprintf(l.r.w, "  %s %s %s in %s — done\n",
		shortDigest(l.digest), l.enc, humanBytes(written), dur.Round(time.Millisecond))
}

func (l *lineLayer) Fail(err error) {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	l.mu.Unlock()

	l.r.mu.Lock()
	defer l.r.mu.Unlock()
	fmt.Fprintf(l.r.w, "  %s %s failed: %v\n", shortDigest(l.digest), l.enc, err)
}

func shortDigest(d digest.Digest) string {
	s := string(d)
	if len(s) > 25 {
		return s[:25]
	}
	return s
}

func humanBytes(n int64) string {
	const (
		KB = 1 << 10
		MB = 1 << 20
		GB = 1 << 30
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.1fGB", float64(n)/GB)
	case n >= MB:
		return fmt.Sprintf("%.1fMB", float64(n)/MB)
	case n >= KB:
		return fmt.Sprintf("%.1fKB", float64(n)/KB)
	}
	return fmt.Sprintf("%dB", n)
}
