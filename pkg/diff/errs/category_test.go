package errs_test

import (
	"testing"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

func TestCategory_ExitCode(t *testing.T) {
	tests := []struct {
		cat  errs.Category
		want int
	}{
		{errs.CategoryInternal, 1},
		{errs.CategoryUser, 2},
		{errs.CategoryEnvironment, 3},
		{errs.CategoryContent, 4},
	}
	for _, tc := range tests {
		if got := tc.cat.ExitCode(); got != tc.want {
			t.Errorf("%s.ExitCode() = %d, want %d", tc.cat, got, tc.want)
		}
	}
}

func TestCategory_String(t *testing.T) {
	cases := map[errs.Category]string{
		errs.CategoryInternal:    "internal",
		errs.CategoryUser:        "user",
		errs.CategoryEnvironment: "environment",
		errs.CategoryContent:     "content",
	}
	for cat, want := range cases {
		if got := cat.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", cat, got, want)
		}
	}
}
