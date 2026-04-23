package errs_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"testing"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

type typedErr struct{ cat errs.Category }

func (e *typedErr) Error() string           { return "typed" }
func (e *typedErr) Category() errs.Category { return e.cat }

type hintErr struct{ msg string }

func (e *hintErr) Error() string           { return "hint" }
func (e *hintErr) Category() errs.Category { return errs.CategoryUser }
func (e *hintErr) NextAction() string      { return e.msg }

func TestClassify_TypedError(t *testing.T) {
	err := &typedErr{cat: errs.CategoryContent}
	cat, hint := errs.Classify(err)
	if cat != errs.CategoryContent {
		t.Errorf("cat = %s, want content", cat)
	}
	if hint != "" {
		t.Errorf("hint = %q, want empty", hint)
	}
}

func TestClassify_WrappedTypedError(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", &typedErr{cat: errs.CategoryUser})
	cat, _ := errs.Classify(wrapped)
	if cat != errs.CategoryUser {
		t.Errorf("cat = %s, want user", cat)
	}
}

func TestClassify_HintFromAdvised(t *testing.T) {
	cat, hint := errs.Classify(&hintErr{msg: "install zstd"})
	if cat != errs.CategoryUser {
		t.Errorf("cat = %s, want user", cat)
	}
	if hint != "install zstd" {
		t.Errorf("hint = %q, want %q", hint, "install zstd")
	}
}

func TestClassify_ContextDeadlineExceeded_IsEnvironment(t *testing.T) {
	cat, _ := errs.Classify(context.DeadlineExceeded)
	if cat != errs.CategoryEnvironment {
		t.Errorf("cat = %s, want environment", cat)
	}
}

func TestClassify_ContextCanceled_IsEnvironment(t *testing.T) {
	cat, _ := errs.Classify(context.Canceled)
	if cat != errs.CategoryEnvironment {
		t.Errorf("cat = %s, want environment", cat)
	}
}

func TestClassify_NetError_IsEnvironment(t *testing.T) {
	netErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	cat, _ := errs.Classify(netErr)
	if cat != errs.CategoryEnvironment {
		t.Errorf("cat = %s, want environment", cat)
	}
}

func TestClassify_URLError_IsEnvironment(t *testing.T) {
	urlErr := &url.Error{Op: "Get", URL: "https://registry.example.com/v2/", Err: errors.New("connection refused")}
	cat, hint := errs.Classify(urlErr)
	if cat != errs.CategoryEnvironment {
		t.Errorf("cat = %s, want environment", cat)
	}
	if hint != "network error talking to registry; check connectivity and --authfile" {
		t.Errorf("hint = %q, want registry connectivity hint", hint)
	}
}

func TestClassify_PathError_IsEnvironment(t *testing.T) {
	pe := &fs.PathError{Op: "open", Path: "/nope", Err: fs.ErrNotExist}
	cat, _ := errs.Classify(pe)
	if cat != errs.CategoryEnvironment {
		t.Errorf("cat = %s, want environment", cat)
	}
}

func TestClassify_Nil(t *testing.T) {
	cat, hint := errs.Classify(nil)
	if cat != errs.CategoryInternal || hint != "" {
		t.Errorf("classify(nil) = (%s, %q), want (internal, \"\")", cat, hint)
	}
}

func TestClassify_UnknownError_DefaultsInternal(t *testing.T) {
	cat, _ := errs.Classify(errors.New("mysterious"))
	if cat != errs.CategoryInternal {
		t.Errorf("cat = %s, want internal", cat)
	}
}
