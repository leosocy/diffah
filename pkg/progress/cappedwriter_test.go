package progress

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestCappedWriter_ClampsAtTotal(t *testing.T) {
	var got []int64
	sink := func(n int64) { got = append(got, n) }
	w := CappedWriter(10, sink)

	w(4)
	w(4)
	w(4)

	want := []int64{4, 4, 2}
	if len(got) != len(want) {
		t.Fatalf("len(got)=%d want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%d want %d", i, got[i], want[i])
		}
	}
}

func TestCappedWriter_DropsAfterCap(t *testing.T) {
	var got []int64
	sink := func(n int64) { got = append(got, n) }
	w := CappedWriter(5, sink)

	w(10) // clamped to 5
	w(3)  // dropped — already at cap

	if len(got) != 1 {
		t.Fatalf("len(got)=%d want 1 (got=%v)", len(got), got)
	}
	if got[0] != 5 {
		t.Errorf("got[0]=%d want 5", got[0])
	}
}

func TestCountingReader_ReportsChunks(t *testing.T) {
	src := bytes.NewReader([]byte("0123456789"))
	var reports []int64
	r := &CountingReader{
		R:       src,
		OnChunk: func(n int64) { reports = append(reports, n) },
	}

	buf := make([]byte, 3)
	totalRead := 0
	for {
		n, err := r.Read(buf)
		totalRead += n
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}

	if totalRead != 10 {
		t.Errorf("totalRead=%d want 10", totalRead)
	}
	// 10-byte source consumed in 3-byte chunks → 3,3,3,1.
	want := []int64{3, 3, 3, 1}
	if len(reports) != len(want) {
		t.Fatalf("len(reports)=%d want %d (reports=%v)", len(reports), len(want), reports)
	}
	for i := range want {
		if reports[i] != want[i] {
			t.Errorf("reports[%d]=%d want %d", i, reports[i], want[i])
		}
	}
}

func TestCountingReader_NilOnChunkSafe(t *testing.T) {
	src := bytes.NewReader([]byte("hello"))
	r := &CountingReader{R: src, OnChunk: nil}

	buf := make([]byte, 5)
	n, err := r.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Read: %v", err)
	}
	if n != 5 {
		t.Errorf("n=%d want 5", n)
	}
	if string(buf[:n]) != "hello" {
		t.Errorf("buf=%q want %q", buf[:n], "hello")
	}
}
