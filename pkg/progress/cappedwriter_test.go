package progress

import "testing"

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
