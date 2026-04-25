package exporter

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/progress"
)

func TestReadAllReportingChunks_NilCallbackEqualsReadAll(t *testing.T) {
	src := []byte("hello world")
	got, err := readAllReportingChunks(bytes.NewReader(src), nil)
	require.NoError(t, err)
	require.Equal(t, src, got)
}

func TestReadAllReportingChunks_ReportsEveryChunk(t *testing.T) {
	src := []byte("0123456789abcdef")
	// OneByteReader forces one byte per Read call → 16 chunk callbacks.
	r := iotest.OneByteReader(bytes.NewReader(src))
	var chunks []int64
	got, err := readAllReportingChunks(r, func(n int64) { chunks = append(chunks, n) })
	require.NoError(t, err)
	require.Equal(t, src, got)
	require.Len(t, chunks, len(src), "onChunk should fire once per Read")
	for _, n := range chunks {
		require.Equal(t, int64(1), n)
	}
}

func TestCappedWriter_ClampsToTotal(t *testing.T) {
	var got []int64
	w := cappedWriter(10, func(n int64) { got = append(got, n) })
	w(4) // within cap → report 4
	w(8) // would push total to 12 → report 6 (cap at 10)
	w(5) // already at cap → drop
	require.Equal(t, []int64{4, 6}, got)

	var sum int64
	for _, n := range got {
		sum += n
	}
	require.Equal(t, int64(10), sum, "capped stream must sum exactly to total")
}

type recordingReporter struct {
	mu     sync.Mutex
	layers []*recordingLayer
}

func (r *recordingReporter) Phase(string) {}
func (r *recordingReporter) StartLayer(d digest.Digest, total int64, _ string) progress.Layer {
	l := &recordingLayer{digest: d, total: total}
	r.mu.Lock()
	r.layers = append(r.layers, l)
	r.mu.Unlock()
	return l
}
func (r *recordingReporter) Finish() {}

type recordingLayer struct {
	digest digest.Digest
	total  int64
	writes []int64
	done   bool
	failed bool
}

func (l *recordingLayer) Written(n int64) { l.writes = append(l.writes, n) }
func (l *recordingLayer) Done()           { l.done = true }
func (l *recordingLayer) Fail(error)      { l.failed = true }

func TestEncodeShipped_StreamsWrittenDuringRead(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	ctx := context.Background()

	plan, err := planPair(ctx, Pair{
		Name:        "svc-a",
		BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef:   "oci-archive:../../testdata/fixtures/v2_oci.tar",
	}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)

	pool := newBlobPool()
	seedManifestAndConfig(pool, plan)
	for _, s := range plan.Shipped {
		pool.countShipped(s.Digest)
	}

	rep := &recordingReporter{}
	require.NoError(t,
		encodeShipped(ctx, pool, []*pairPlan{plan}, "auto", DefaultFingerprinter{}, rep, 0, 0, 0))

	require.NotEmpty(t, rep.layers, "encodeShipped should have started at least one layer")
	for _, layer := range rep.layers {
		require.True(t, layer.done, "layer %s should be marked done", layer.digest)
		require.GreaterOrEqual(t, len(layer.writes), 1,
			"layer %s: Written() must be called at least once so the bar animates",
			layer.digest)
		var total int64
		for _, n := range layer.writes {
			total += n
		}
		require.Equal(t, layer.total, total,
			"layer %s: sum of Written chunks (%d) must equal declared total (%d)",
			layer.digest, total, layer.total)
	}
}

func TestEncodeShipped_WarningOnError_FallbackToFull(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	ctx := context.Background()

	plan, err := planPair(ctx, Pair{
		Name:        "svc-a",
		BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef:   "oci-archive:../../testdata/fixtures/v2_oci.tar",
	}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)

	fakeDigest := digest.Digest("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	for i := range plan.BaselineLayerMeta {
		plan.BaselineLayerMeta[i].Digest = fakeDigest
	}

	pool := newBlobPool()
	seedManifestAndConfig(pool, plan)
	for _, s := range plan.Shipped {
		pool.countShipped(s.Digest)
	}

	var buf bytes.Buffer
	err = encodeShipped(ctx, pool, []*pairPlan{plan}, "auto", DefaultFingerprinter{}, progress.NewLine(&buf), 0, 0, 0)
	require.NoError(t, err, "encodeShipped must tolerate per-layer errors")

	for _, s := range plan.Shipped {
		entry, ok := pool.entries[s.Digest]
		require.True(t, ok, "shipped blob must be in pool")
		require.Equal(t, diff.EncodingFull, entry.Encoding, "fallback must be full encoding")
	}
}
