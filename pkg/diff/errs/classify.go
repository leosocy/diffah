package errs

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/url"
)

// Classify inspects err and returns its Category and an optional hint
// describing a remediation step. If err is nil it returns
// (CategoryInternal, ""). Errors that implement Categorized use their own
// category; otherwise the function falls back to heuristics based on
// standard-library error types (context, net, url, fs).
func Classify(err error) (Category, string) {
	if err == nil {
		return CategoryInternal, ""
	}
	var cat Categorized
	if errors.As(err, &cat) {
		var adv Advised
		if errors.As(err, &adv) {
			return cat.Category(), adv.NextAction()
		}
		return cat.Category(), ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return CategoryEnvironment, "operation was cancelled or timed out"
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return CategoryEnvironment,
			"network error talking to registry; check connectivity and --authfile"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return CategoryEnvironment,
			"network error talking to registry; check connectivity and --authfile"
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return CategoryEnvironment, "filesystem error: " + pathErr.Path
	}
	return CategoryInternal, ""
}
