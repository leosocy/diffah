package errs

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/url"
	"os"
	"strings"
)

func Classify(err error) (Category, string) {
	if err == nil {
		return CategoryInternal, ""
	}
	var cat Categorized
	if errors.As(err, &cat) {
		var adv Advised
		hint := ""
		if errors.As(err, &adv) {
			hint = adv.NextAction()
		}
		return cat.Category(), hint
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return CategoryEnvironment, "operation was cancelled or timed out"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return CategoryEnvironment,
			"network error talking to registry; check connectivity and --authfile"
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return CategoryEnvironment,
			"network error talking to registry; check connectivity and --authfile"
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return CategoryEnvironment, "filesystem error: " + pathErr.Path
	}
	if errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist) {
		return CategoryEnvironment, "filesystem error"
	}
	if isCobraUserError(err) {
		return CategoryUser, "run 'diffah --help' for usage"
	}
	return CategoryInternal, ""
}

func isCobraUserError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unknown command") ||
		strings.Contains(msg, "unknown flag") ||
		strings.Contains(msg, "unknown shorthand") ||
		strings.Contains(msg, "required flag") ||
		strings.Contains(msg, "flag needs an argument") ||
		strings.Contains(msg, "bad flag syntax") ||
		strings.Contains(msg, "arg(s)")
}
