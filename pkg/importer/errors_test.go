package importer

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

func TestErrMissingPatchSource_Format(t *testing.T) {
	err := &ErrMissingPatchSource{
		ImageName:       "svc-a",
		ShippedDigest:   digest.Digest("sha256:aaa"),
		PatchFromDigest: digest.Digest("sha256:bbb"),
	}
	got := err.Error()
	if !strings.Contains(got, "svc-a") {
		t.Errorf("Error() must include image name; got %q", got)
	}
	if !strings.Contains(got, "sha256:bbb") {
		t.Errorf("Error() must include patch source digest; got %q", got)
	}
	if !strings.Contains(got, "patch source") {
		t.Errorf("Error() must mention 'patch source'; got %q", got)
	}
}

func TestErrMissingBaselineReuseLayer_Format(t *testing.T) {
	err := &ErrMissingBaselineReuseLayer{
		ImageName:   "svc-b",
		LayerDigest: digest.Digest("sha256:ccc"),
	}
	got := err.Error()
	if !strings.Contains(got, "svc-b") {
		t.Errorf("Error() must include image name; got %q", got)
	}
	if !strings.Contains(got, "sha256:ccc") {
		t.Errorf("Error() must include layer digest; got %q", got)
	}
}

func TestErrApplyInvariantFailed_Format(t *testing.T) {
	err := &ErrApplyInvariantFailed{
		ImageName:  "svc-c",
		Missing:    []digest.Digest{"sha256:ddd"},
		Unexpected: nil,
		Reason:     "layer count mismatch",
	}
	got := err.Error()
	if !strings.Contains(got, "svc-c") {
		t.Errorf("Error() must include image name; got %q", got)
	}
	if !strings.Contains(got, "layer count mismatch") {
		t.Errorf("Error() must include reason; got %q", got)
	}
}

// TestSentinels_Categorized verifies that errs.Classify routes each new
// importer sentinel to CategoryContent and surfaces a non-empty remediation
// hint (proving the sentinel implements both Categorized and Advised).
func TestSentinels_Categorized(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "ErrMissingPatchSource",
			err: &ErrMissingPatchSource{
				ImageName:       "svc-a",
				ShippedDigest:   digest.Digest("sha256:aaa"),
				PatchFromDigest: digest.Digest("sha256:bbb"),
			},
		},
		{
			name: "ErrMissingBaselineReuseLayer",
			err: &ErrMissingBaselineReuseLayer{
				ImageName:   "svc-b",
				LayerDigest: digest.Digest("sha256:ccc"),
			},
		},
		{
			name: "ErrApplyInvariantFailed",
			err: &ErrApplyInvariantFailed{
				ImageName: "svc-c",
				Reason:    "layer count mismatch",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, hint := errs.Classify(tc.err)
			if cat != errs.CategoryContent {
				t.Errorf("Classify category = %v, want CategoryContent", cat)
			}
			if hint == "" {
				t.Errorf("Classify hint must be non-empty for %T", tc.err)
			}
		})
	}
}

// TestIsBlobNotFound exercises the predicate against the three real-world
// "blob missing" shapes (os.IsNotExist, docker-archive "Unknown blob",
// registry "blob unknown") plus negatives covering the conservative gap
// (registry 404 with non-JSON body) and unrelated auth/network/url shapes
// that must keep their existing classification.
func TestIsBlobNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// TRUE: real "blob missing" signals.
		{
			name: "os.ErrNotExist direct",
			err:  os.ErrNotExist,
			want: true,
		},
		{
			name: "PathError ENOENT (file-backed transport)",
			err:  &os.PathError{Op: "open", Path: "x", Err: syscall.ENOENT},
			want: true,
		},
		{
			name: "docker-archive Unknown blob shape",
			err:  fmt.Errorf("Unknown blob sha256:abc"),
			want: true,
		},
		{
			name: "registry blob unknown shape",
			err:  fmt.Errorf("fetching blob: blob unknown to registry"),
			want: true,
		},
		{
			name: "wrapped os.ErrNotExist",
			err:  fmt.Errorf("ctx: %w", os.ErrNotExist),
			want: true,
		},

		// FALSE: nil, plain strings, and other classes that must NOT be
		// reclassified as B2 (auth, network, url, conservative-gap 404).
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "plain error with no needle",
			err:  errors.New("boom"),
			want: false,
		},
		{
			name: "url.Error network shape",
			err:  &url.Error{Op: "Get", URL: "x", Err: errors.New("dns")},
			want: false,
		},
		{
			name: "auth-class unauthorized",
			err:  errors.New("unauthorized"),
			want: false,
		},
		{
			name: "conservative-gap registry 404 non-JSON body",
			err:  errors.New("error parsing HTTP 404 response body: unexpected EOF"),
			want: false,
		},
		{
			name: "network-class connection reset",
			err:  errors.New("connection reset by peer"),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isBlobNotFound(tc.err)
			if got != tc.want {
				t.Errorf("isBlobNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
