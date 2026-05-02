package cmd

import "testing"

func TestParseMemoryBudget_Table(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"0", 0, false},
		{"512", 512, false},
		{"1KiB", 1 << 10, false},
		{"1MiB", 1 << 20, false},
		{"8GiB", 8 << 30, false},
		{"1KB", 1000, false},
		{"4GB", 4_000_000_000, false},
		{"  2gib  ", 2 << 30, false},
		{"-1", 0, true},
		{"abc", 0, true},
		{"", 0, true},
		{"5XB", 0, true},
	}
	for _, c := range cases {
		got, err := parseMemoryBudget(c.in)
		if (err != nil) != c.err {
			t.Errorf("parseMemoryBudget(%q): err=%v want err=%v", c.in, err, c.err)
		}
		if !c.err && got != c.want {
			t.Errorf("parseMemoryBudget(%q): got %d want %d", c.in, got, c.want)
		}
	}
}
