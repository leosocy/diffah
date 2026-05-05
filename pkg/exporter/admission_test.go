package exporter

import "testing"

func TestEstimateRSSForWindowLog_TableIsConservative(t *testing.T) {
	cases := []struct {
		windowLog int
		min       int64
	}{
		{27, 256 << 20},
		{30, 2 << 30},
		{31, 4 << 30},
	}
	for _, c := range cases {
		got := estimateRSSForWindowLog(c.windowLog)
		if got < c.min {
			t.Errorf("windowLog=%d: estimate %d < min %d", c.windowLog, got, c.min)
		}
	}
	// Out-of-table values fall back to the largest entry.
	if got := estimateRSSForWindowLog(99); got < (4 << 30) {
		t.Errorf("out-of-table fallback too small: %d", got)
	}
}
