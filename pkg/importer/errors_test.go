package importer

import (
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
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
