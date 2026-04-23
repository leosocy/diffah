package progress

import (
	"io"
	"sync"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

func NewBars(w io.Writer) Reporter {
	if w == nil {
		return NewDiscard()
	}
	p := mpb.New(
		mpb.WithOutput(w),
		mpb.WithRefreshRate(100*time.Millisecond),
	)
	return &barsReporter{p: p, underlying: w}
}

type barsReporter struct {
	p          *mpb.Progress
	underlying io.Writer
}

func (r *barsReporter) Phase(name string) {
	_, _ = r.p.Write([]byte("[" + name + "]\n"))
}

func (r *barsReporter) StartLayer(d digest.Digest, total int64, enc string) Layer {
	name := shortDigest(d) + " " + enc
	bar := r.p.AddBar(total,
		mpb.PrependDecorators(
			decor.Name(name, decor.WC{W: 30, C: decor.DindentRight}),
		),
		mpb.AppendDecorators(
			decor.CountersKibiByte("% .1f / % .1f"),
			decor.Name(" "),
			decor.AverageSpeed(decor.SizeB1024(0), "% .1f"),
			decor.Name(" "),
			decor.EwmaETA(decor.ET_STYLE_GO, 30),
			decor.OnComplete(decor.Name(""), " ✓"),
		),
	)
	return &barsLayer{bar: bar, total: total}
}

func (r *barsReporter) Finish() {
	r.p.Wait()
}

func (r *barsReporter) SlogWriter() io.Writer {
	return &slogWriter{p: r.p, fallback: r.underlying}
}

type slogWriter struct {
	p        *mpb.Progress
	fallback io.Writer
}

func (sw *slogWriter) Write(b []byte) (int, error) {
	n, err := sw.p.Write(b)
	if err != nil {
		return sw.fallback.Write(b)
	}
	return n, nil
}

type barsLayer struct {
	bar   *mpb.Bar
	total int64
	mu    sync.Mutex
	done  bool
}

func (l *barsLayer) Written(n int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return
	}
	l.bar.IncrInt64(n)
}

func (l *barsLayer) Done() {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	l.mu.Unlock()
	if cur := l.bar.Current(); cur < l.total {
		l.bar.IncrInt64(l.total - cur)
	}
}

func (l *barsLayer) Fail(_ error) {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	l.mu.Unlock()
	l.bar.Abort(false)
}
