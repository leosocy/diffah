package importer

import (
	"fmt"
	"io"
)

// ApplyImageStatus is the per-image outcome categorization for the Stage-4
// final summary. Values are mutually exclusive; one is recorded per image
// in the bundle.
type ApplyImageStatus int

const (
	// ApplyImageOK means composeImage succeeded and verifyApplyInvariant
	// passed.
	ApplyImageOK ApplyImageStatus = iota
	// ApplyImageFailedCompose means composeImage returned an error
	// before producing a complete dest manifest. The dest may be
	// partially populated.
	ApplyImageFailedCompose
	// ApplyImageFailedInvariant means composeImage produced a dest
	// manifest but the post-apply invariant check rejected it. The
	// dest is fully written but does not match the sidecar's
	// expectation.
	ApplyImageFailedInvariant
	// ApplyImageSkippedPreflight means RunPreflight rejected the
	// image upfront (PreflightMissingPatchSource,
	// PreflightMissingReuseLayer, or PreflightError) and partial
	// mode skipped it. The dest was not touched.
	ApplyImageSkippedPreflight
)

// ApplyImageResult is the per-image record in the final ApplyReport.
// Err is non-nil for every non-OK status and carries the underlying
// sentinel (ErrMissingPatchSource, ErrMissingBaselineReuseLayer,
// ErrApplyInvariantFailed, or a transport error) so cmd.Execute can
// classify it for exit-code mapping.
type ApplyImageResult struct {
	ImageName string
	Status    ApplyImageStatus
	Err       error
}

// ApplyReport is the aggregated outcome of one Import() call. Total
// counts every image in the bundle (including skipped ones). Results
// is appended in bundle.sidecar.Images iteration order.
type ApplyReport struct {
	Total   int
	Results []ApplyImageResult
}

// Successful returns the number of ApplyImageOK results in the report.
func (r ApplyReport) Successful() int {
	n := 0
	for _, x := range r.Results {
		if x.Status == ApplyImageOK {
			n++
		}
	}
	return n
}

// renderSummary writes the Stage-4 final summary to w. The first line
// is parseable by integration tests ("applied N/M images"). Per-image
// rows follow, then a final advisory paragraph if any image did not
// succeed.
func renderSummary(w io.Writer, r ApplyReport) {
	fmt.Fprintf(w, "diffah: applied %d/%d images\n", r.Successful(), r.Total)
	for _, x := range r.Results {
		switch x.Status {
		case ApplyImageOK:
			fmt.Fprintf(w, "  ok  %s: applied + verified\n", x.ImageName)
		case ApplyImageFailedCompose:
			fmt.Fprintf(w, "  err %s: compose failed: %v\n", x.ImageName, x.Err)
		case ApplyImageFailedInvariant:
			fmt.Fprintf(w, "  err %s: applied with invariant mismatch: %v\n",
				x.ImageName, x.Err)
		case ApplyImageSkippedPreflight:
			fmt.Fprintf(w, "  skip %s: preflight skipped (%v)\n", x.ImageName, x.Err)
		}
	}
	if r.Successful() < r.Total {
		fmt.Fprintf(w, "\nnote: dest may contain partially-written images from this run.\n")
		fmt.Fprintf(w, "manual cleanup is required for any image marked failed/mismatch above.\n")
	}
}
