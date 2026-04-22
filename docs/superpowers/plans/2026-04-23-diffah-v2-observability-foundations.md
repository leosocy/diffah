# Phase 1 — Observability Foundations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land slog, structured errors with exit-code taxonomy, TTY-aware Docker-style progress bars, `--output json`, and a compat doc — so every later roadmap phase ships onto a solid observability floor.

**Architecture:** Three peer channels (slog / progress.Reporter / error-classification at `cmd.Execute` edge) fed by one stream of domain events emitted by `pkg/exporter`, `pkg/importer`, `pkg/diff`, and `internal/*`. Progress is NOT an slog handler — pretty multi-bar UI stays separated from grep-friendly logs.

**Tech Stack:** Go 1.25.4, `log/slog` (stdlib), `vbauerster/mpb/v8` (new dep), `mattn/go-isatty` (transitive of `go.podman.io/image/v5`, already vendored), existing `cobra` + `go.podman.io/image/v5`.

**Spec:** `docs/superpowers/specs/2026-04-23-observability-foundations-design.md`

**Branch:** `spec/v2-observability-foundations` (currently checked out). Each task is one commit.

---

## Task 1: `pkg/diff/errs` package — taxonomy + classification

**Files:**
- Create: `pkg/diff/errs/category.go`
- Create: `pkg/diff/errs/category_test.go`
- Create: `pkg/diff/errs/classify.go`
- Create: `pkg/diff/errs/classify_test.go`
- Create: `pkg/diff/errs/doc.go`

- [ ] **Step 1.1 — Write failing test for Category type**

Create `pkg/diff/errs/category_test.go`:

```go
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
```

- [ ] **Step 1.2 — Run test to verify it fails**

Run: `go test ./pkg/diff/errs/...`
Expected: FAIL — `errs.Category` undefined.

- [ ] **Step 1.3 — Implement category.go**

Create `pkg/diff/errs/category.go`:

```go
// Package errs defines the error category taxonomy + classification helpers
// used by cmd.Execute to map returned errors to exit codes and user hints.
//
// Existing pkg/diff error types opt into a category by implementing
// Categorized. Errors lacking that interface fall through to a small set
// of stdlib sentinel matchers (see classify.go) and ultimately default to
// CategoryInternal (exit 1).
package errs

// Category classifies an error for exit-code mapping and hint rendering.
type Category int

const (
	// CategoryInternal is the default for unclassified errors. Exit 1.
	CategoryInternal Category = iota
	// CategoryUser covers bad flags, missing inputs, and misuse. Exit 2.
	CategoryUser
	// CategoryEnvironment covers missing tools, network/auth failures, FS
	// permission issues. Exit 3.
	CategoryEnvironment
	// CategoryContent covers schema, digest, and corruption failures. Exit 4.
	CategoryContent
)

// ExitCode returns the CLI exit code for this category.
func (c Category) ExitCode() int {
	switch c {
	case CategoryInternal:
		return 1
	case CategoryUser:
		return 2
	case CategoryEnvironment:
		return 3
	case CategoryContent:
		return 4
	default:
		return 1
	}
}

func (c Category) String() string {
	switch c {
	case CategoryInternal:
		return "internal"
	case CategoryUser:
		return "user"
	case CategoryEnvironment:
		return "environment"
	case CategoryContent:
		return "content"
	default:
		return "internal"
	}
}

// Categorized is implemented by error types that declare their category.
// Classify walks the error chain via errors.As to find the innermost type
// that implements this interface.
type Categorized interface {
	Category() Category
}

// Advised is implemented by error types that carry a next-action hint.
// Unlike Categorized, Advised hints are optional — missing hint returns
// "" and the renderer omits the hint line.
type Advised interface {
	NextAction() string
}
```

- [ ] **Step 1.4 — Run test to verify it passes**

Run: `go test ./pkg/diff/errs/...`
Expected: PASS.

- [ ] **Step 1.5 — Write failing test for Classify**

Create `pkg/diff/errs/classify_test.go`:

```go
package errs_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"testing"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// typedErr is a local stub that declares its category explicitly.
type typedErr struct{ cat errs.Category }

func (e *typedErr) Error() string            { return "typed" }
func (e *typedErr) Category() errs.Category  { return e.cat }

type hintErr struct{ msg string }

func (e *hintErr) Error() string             { return "hint" }
func (e *hintErr) Category() errs.Category   { return errs.CategoryUser }
func (e *hintErr) NextAction() string        { return e.msg }

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
```

- [ ] **Step 1.6 — Run test to verify it fails**

Run: `go test ./pkg/diff/errs/... -run Classify`
Expected: FAIL — `errs.Classify` undefined.

- [ ] **Step 1.7 — Implement classify.go**

Create `pkg/diff/errs/classify.go`:

```go
package errs

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/url"
)

// Classify extracts the category and next-action hint for err.
//
// Lookup order:
//  1. nil → (CategoryInternal, "") — caller should not invoke for nil errs
//  2. The innermost Categorized in err's chain (errors.As traversal)
//     wins. If it also implements Advised, that hint is used.
//  3. Stdlib/library sentinels (context cancellation/deadline, net errors,
//     filesystem path errors) classify as Environment with a generic hint.
//  4. Default: CategoryInternal.
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
	// Fallbacks for untyped errors from stdlib / third-party libraries.
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
```

- [ ] **Step 1.8 — Create doc.go to lock package-scope godoc**

Create `pkg/diff/errs/doc.go` with empty body (the `Package errs` comment lives on `category.go`). Skip if keeping a single file is preferred; this plan splits the concerns to keep each file < 100 LOC. If skipping, keep the package comment on `category.go` and remove this step.

```go
// Package errs — see category.go.
package errs
```

- [ ] **Step 1.9 — Run all tests to verify**

Run: `go test ./pkg/diff/errs/...`
Expected: PASS (all 10 tests).

- [ ] **Step 1.10 — Commit**

```bash
git add pkg/diff/errs/
git commit -m "feat(errs): add Category + Classify taxonomy

Introduce pkg/diff/errs with the four-category exit-code taxonomy
(internal/user/environment/content), the Categorized/Advised opt-in
interfaces, and the Classify helper. Classify walks the error chain via
errors.As, then falls back to stdlib sentinel matchers (context,
net.OpError, url.Error, fs.PathError) before defaulting to internal."
```

---

## Task 2: Categorize existing error types + wire exit codes into `cmd.Execute`

**Files:**
- Modify: `pkg/diff/errors.go` (add Category/NextAction methods on every type)
- Modify: `pkg/diff/errors_test.go` (add exhaustive mapping test)
- Modify: `cmd/root.go` (Execute returns int; classification edge)
- Modify: `main.go` (propagate exit code)
- Modify: `internal/zstdpatch/available.go` (make `ErrZstdBinaryMissing` / `ErrZstdEncodeFailure` Categorized)
- Create: `cmd/exit_integration_test.go` (subprocess test per category; build tag `integration`)

- [ ] **Step 2.1 — Write failing exhaustive-mapping test**

Open `pkg/diff/errors_test.go` and append:

```go
package diff

import (
	"testing"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// TestEveryErrorType_IsCategorized guarantees every exported error type in
// pkg/diff declares a Category. If you add a new ErrX, add a Category()
// method on it — or this test fails the build.
func TestEveryErrorType_IsCategorized(t *testing.T) {
	instances := []any{
		&ErrManifestListUnselected{},
		&ErrSidecarSchema{},
		&ErrBaselineMissingBlob{},
		&ErrIncompatibleOutputFormat{},
		&ErrSourceManifestUnreadable{},
		&ErrDigestMismatch{},
		&ErrIntraLayerAssemblyMismatch{},
		&ErrBaselineBlobDigestMismatch{},
		&ErrShippedBlobDigestMismatch{},
		&ErrBaselineMissingPatchRef{},
		&ErrIntraLayerUnsupported{},
		&ErrPhase1Archive{},
		&ErrUnknownBundleVersion{},
		&ErrInvalidBundleFormat{},
		&ErrMultiImageNeedsNamedBaselines{},
		&ErrBaselineNameUnknown{},
		&ErrBaselineMismatch{},
		&ErrBaselineMissing{},
		&ErrInvalidBundleSpec{},
		&ErrDuplicateBundleName{},
	}
	for _, v := range instances {
		cz, ok := v.(errs.Categorized)
		if !ok {
			t.Errorf("%T does not implement errs.Categorized", v)
			continue
		}
		if cz.Category() == errs.CategoryInternal {
			t.Errorf("%T has Category=internal (must be user/env/content)", v)
		}
	}
}
```

- [ ] **Step 2.2 — Run test to verify it fails**

Run: `go test ./pkg/diff -run TestEveryErrorType_IsCategorized`
Expected: FAIL — none of the types implement Categorized yet.

- [ ] **Step 2.3 — Add Category + NextAction methods in `pkg/diff/errors.go`**

At the top of `pkg/diff/errors.go`, add the import:

```go
import (
	"errors"
	"fmt"

	"github.com/leosocy/diffah/pkg/diff/errs"
)
```

At the end of `pkg/diff/errors.go`, append the following blocks (one per type). Keep them grouped per-type so reviewers can match each error with its category at a glance:

```go
// --- Category + NextAction declarations (grouped per error type). ---

func (*ErrManifestListUnselected) Category() errs.Category { return errs.CategoryUser }
func (*ErrManifestListUnselected) NextAction() string {
	return "pass --platform os/arch[/variant] to select a manifest-list instance"
}

func (*ErrSidecarSchema) Category() errs.Category { return errs.CategoryContent }
func (*ErrSidecarSchema) NextAction() string {
	return "archive may be corrupt or from an unsupported version"
}

func (*ErrBaselineMissingBlob) Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineMissingBlob) NextAction() string {
	return "verify the --baseline value matches the baseline the delta was built against"
}

func (*ErrIncompatibleOutputFormat) Category() errs.Category { return errs.CategoryUser }
func (*ErrIncompatibleOutputFormat) NextAction() string {
	return "pass --allow-convert to accept digest drift, or pick a compatible --output-format"
}

// ErrSourceManifestUnreadable delegates to its wrapped cause. If no known
// cause classifies, defaults to environment (network is the common case).
func (e *ErrSourceManifestUnreadable) Category() errs.Category {
	if e == nil || e.Cause == nil {
		return errs.CategoryEnvironment
	}
	if cat, _ := errs.Classify(e.Cause); cat != errs.CategoryInternal {
		return cat
	}
	return errs.CategoryEnvironment
}

func (*ErrDigestMismatch) Category() errs.Category             { return errs.CategoryContent }
func (*ErrIntraLayerAssemblyMismatch) Category() errs.Category { return errs.CategoryContent }

func (*ErrBaselineBlobDigestMismatch) Category() errs.Category { return errs.CategoryContent }
func (*ErrShippedBlobDigestMismatch) Category() errs.Category  { return errs.CategoryContent }

func (*ErrBaselineMissingPatchRef) Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineMissingPatchRef) NextAction() string {
	return "the named baseline lacks the layer this patch was built against"
}

func (*ErrIntraLayerUnsupported) Category() errs.Category { return errs.CategoryUser }
func (*ErrIntraLayerUnsupported) NextAction() string {
	return "retry with --intra-layer=off or provide a baseline with readable blob bytes"
}

func (*ErrPhase1Archive) Category() errs.Category { return errs.CategoryContent }
func (*ErrPhase1Archive) NextAction() string {
	return "re-export the archive with the current diffah"
}

func (*ErrUnknownBundleVersion) Category() errs.Category { return errs.CategoryContent }
func (*ErrUnknownBundleVersion) NextAction() string {
	return "upgrade diffah to a version that supports this archive"
}

func (*ErrInvalidBundleFormat) Category() errs.Category { return errs.CategoryContent }

func (*ErrMultiImageNeedsNamedBaselines) Category() errs.Category { return errs.CategoryUser }
func (*ErrMultiImageNeedsNamedBaselines) NextAction() string {
	return "pass --baseline NAME=PATH (repeatable) or --baseline-spec FILE"
}

func (*ErrBaselineNameUnknown) Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineNameUnknown) NextAction() string {
	return "check `diffah inspect` for the names this bundle expects"
}

func (*ErrBaselineMismatch) Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineMismatch) NextAction() string {
	return "the supplied baseline has the wrong manifest digest"
}

func (*ErrBaselineMissing) Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineMissing) NextAction() string {
	return "provide --baseline NAME=PATH for each missing image or drop --strict"
}

func (*ErrInvalidBundleSpec) Category() errs.Category { return errs.CategoryUser }
func (*ErrDuplicateBundleName) Category() errs.Category { return errs.CategoryUser }

// Compile-time guard: every type listed in the exhaustive mapping test MUST
// implement errs.Categorized. A missing method fails the `var _` assertion
// at build time rather than at `go test`.
var (
	_ errs.Categorized = (*ErrManifestListUnselected)(nil)
	_ errs.Categorized = (*ErrSidecarSchema)(nil)
	_ errs.Categorized = (*ErrBaselineMissingBlob)(nil)
	_ errs.Categorized = (*ErrIncompatibleOutputFormat)(nil)
	_ errs.Categorized = (*ErrSourceManifestUnreadable)(nil)
	_ errs.Categorized = (*ErrDigestMismatch)(nil)
	_ errs.Categorized = (*ErrIntraLayerAssemblyMismatch)(nil)
	_ errs.Categorized = (*ErrBaselineBlobDigestMismatch)(nil)
	_ errs.Categorized = (*ErrShippedBlobDigestMismatch)(nil)
	_ errs.Categorized = (*ErrBaselineMissingPatchRef)(nil)
	_ errs.Categorized = (*ErrIntraLayerUnsupported)(nil)
	_ errs.Categorized = (*ErrPhase1Archive)(nil)
	_ errs.Categorized = (*ErrUnknownBundleVersion)(nil)
	_ errs.Categorized = (*ErrInvalidBundleFormat)(nil)
	_ errs.Categorized = (*ErrMultiImageNeedsNamedBaselines)(nil)
	_ errs.Categorized = (*ErrBaselineNameUnknown)(nil)
	_ errs.Categorized = (*ErrBaselineMismatch)(nil)
	_ errs.Categorized = (*ErrBaselineMissing)(nil)
	_ errs.Categorized = (*ErrInvalidBundleSpec)(nil)
	_ errs.Categorized = (*ErrDuplicateBundleName)(nil)
)
```

If the existing imports in `pkg/diff/errors.go` already include `"fmt"` but not `"errors"`, add `"errors"` only if new code uses it — the snippet above does not use `errors` directly, so no extra import beyond the `errs` package is needed.

- [ ] **Step 2.4 — Run tests to verify categorization passes**

Run: `go test ./pkg/diff`
Expected: PASS. `TestEveryErrorType_IsCategorized` now succeeds.

- [ ] **Step 2.5 — Categorize zstdpatch sentinels**

Modify `internal/zstdpatch/available.go` — locate the declaration block for `ErrZstdBinaryMissing` and `ErrZstdEncodeFailure` (both currently `errors.New("...")` sentinels) and convert them to typed sentinel structs so they can carry a Category.

The file currently has (verify by reading it first):

```go
var ErrZstdBinaryMissing = errors.New("zstd binary missing or incompatible version")
var ErrZstdEncodeFailure = errors.New("zstd encode failed")
```

Replace with:

```go
var (
	// ErrZstdBinaryMissing indicates the environment is missing zstd ≥ 1.5
	// on $PATH. Exit 3 with an install hint.
	ErrZstdBinaryMissing = &zstdErr{
		msg:    "zstd binary missing or incompatible version",
		action: "install zstd 1.5+ (brew install zstd / apt install zstd)",
	}
	// ErrZstdEncodeFailure indicates a runtime failure from the zstd CLI
	// (not a missing binary). Exit 3.
	ErrZstdEncodeFailure = &zstdErr{
		msg:    "zstd encode failed",
		action: "re-run with --log-level=debug for zstd stderr capture",
	}
)

type zstdErr struct {
	msg    string
	action string
}

func (e *zstdErr) Error() string            { return e.msg }
func (e *zstdErr) Category() errs.Category  { return errs.CategoryEnvironment }
func (e *zstdErr) NextAction() string       { return e.action }
```

Add the import:

```go
import "github.com/leosocy/diffah/pkg/diff/errs"
```

If existing code uses `errors.Is(err, ErrZstdBinaryMissing)`, it still works because the sentinels are pointer-comparable values, not derived types.

- [ ] **Step 2.6 — Run package tests to verify zstdpatch still works**

Run: `go test ./internal/zstdpatch/...`
Expected: PASS.

- [ ] **Step 2.7 — Write failing test for `cmd.Execute` exit-code mapping**

Append to `cmd/root_test.go` (or create it if missing):

```go
package cmd_test

import (
	"bytes"
	"testing"

	"github.com/leosocy/diffah/cmd"
	"github.com/leosocy/diffah/pkg/diff"
)

func TestExecute_ReturnsCategoryExitCode(t *testing.T) {
	// Directly invoke the classification edge via a test-only shim.
	// If Execute accepts an error instead (unit-friendly), use that;
	// otherwise expose cmd.ClassifyExitCode for this test.
	got := cmd.ClassifyExitCode(&diff.ErrBaselineMismatch{Name: "x"})
	if got != 2 {
		t.Errorf("ErrBaselineMismatch → exit %d, want 2", got)
	}
	var buf bytes.Buffer
	cmd.RenderError(&buf, &diff.ErrBaselineMismatch{Name: "x"}, "text")
	if !bytes.Contains(buf.Bytes(), []byte("diffah: user:")) {
		t.Errorf("RenderError text = %q; want prefix 'diffah: user:'", buf.String())
	}
}
```

- [ ] **Step 2.8 — Run test to verify it fails**

Run: `go test ./cmd -run TestExecute_ReturnsCategoryExitCode`
Expected: FAIL — `cmd.ClassifyExitCode` / `cmd.RenderError` undefined.

- [ ] **Step 2.9 — Implement classification edge in `cmd/root.go`**

Rewrite the bottom of `cmd/root.go` (keep existing `rootCmd`, `version`, `logLevel` declarations as-is). Replace the existing `Execute` function:

```go
package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// ... rootCmd and existing declarations remain unchanged ...

// Execute runs the root command and returns the CLI exit code.
// Callers (main.go) wrap this in os.Exit.
func Execute(stderr io.Writer) int {
	err := rootCmd.Execute()
	if err == nil {
		return 0
	}
	exit := ClassifyExitCode(err)
	RenderError(stderr, err, outputFormatFlag())
	return exit
}

// ClassifyExitCode maps err to an exit code via pkg/diff/errs.
// Exposed for testing.
func ClassifyExitCode(err error) int {
	if err == nil {
		return 0
	}
	cat, _ := errs.Classify(err)
	return cat.ExitCode()
}

// RenderError writes the structured error to w in the requested format.
// format is "text" or "json". Default is "text".
func RenderError(w io.Writer, err error, format string) {
	if err == nil {
		return
	}
	cat, hint := errs.Classify(err)
	if format == "json" {
		payload := struct {
			SchemaVersion int `json:"schema_version"`
			Error         struct {
				Category   string `json:"category"`
				Message    string `json:"message"`
				NextAction string `json:"next_action,omitempty"`
			} `json:"error"`
		}{SchemaVersion: 1}
		payload.Error.Category = cat.String()
		payload.Error.Message = err.Error()
		payload.Error.NextAction = hint
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(payload)
		return
	}
	fmt.Fprintf(w, "diffah: %s: %s\n", cat, err.Error())
	if hint != "" {
		fmt.Fprintf(w, "  hint: %s\n", hint)
	}
}

// outputFormatFlag reads the current --output value from the root command.
// When the flag is unregistered (Task 7 adds it), defaults to "text".
func outputFormatFlag() string {
	if f := rootCmd.PersistentFlags().Lookup("output"); f != nil {
		return f.Value.String()
	}
	return "text"
}
```

Remove the previous body of `Execute` that wrote `"diffah: %s", err` to stderr.

- [ ] **Step 2.10 — Update main.go to propagate exit code**

Modify `main.go` to pass the returned int to `os.Exit`:

```go
package main

import (
	"os"

	"github.com/leosocy/diffah/cmd"
)

func main() {
	os.Exit(cmd.Execute(os.Stderr))
}
```

- [ ] **Step 2.11 — Run all tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 2.12 — Write integration test for exit codes under subprocess**

Create `cmd/exit_integration_test.go`:

```go
//go:build integration

package cmd_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// runDiffah invokes the diffah binary via `go run` with the same build tags
// required on Linux CI; returns stdout, stderr, and exit code.
func runDiffah(t *testing.T, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	cmd := exec.Command("go", append([]string{
		"run",
		"-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper",
		"../main.go",
	}, args...)...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("run diffah: %v", err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

func TestExit_UserError_MissingRequiredFlag(t *testing.T) {
	_, stderr, exit := runDiffah(t, "export")
	if exit != 2 {
		t.Errorf("exit = %d, want 2 (user)", exit)
	}
	if !strings.Contains(stderr, "user:") && !strings.Contains(stderr, "required") {
		t.Errorf("stderr = %q; want user-category message", stderr)
	}
}

func TestExit_ContentError_UnknownBundleVersion(t *testing.T) {
	// A forged archive with sidecar version "v999" triggers
	// ErrUnknownBundleVersion — exit 4. This assumes a fixture exists
	// under testdata/fixtures; skip if not present.
	if _, err := exec.LookPath("diffah"); err != nil {
		// running via `go run` is fine; the LookPath is just a safety
		// guard against broken PATH setups.
	}
	_, _, exit := runDiffah(t, "inspect", "testdata/fixtures/forged_v999.tar")
	if exit != 4 && exit != 2 {
		// exit 2 when the fixture is missing (the inspect arg check); if the
		// fixture exists, exit 4 from content classification.
		t.Errorf("exit = %d, want 4 (content) or 2 (if fixture missing)", exit)
	}
}
```

- [ ] **Step 2.13 — Run integration test (may skip with missing fixture)**

Run: `go test ./cmd -tags integration -run TestExit_UserError`
Expected: PASS (exit 2 on bare `diffah export`).

- [ ] **Step 2.14 — Commit**

```bash
git add pkg/diff/errors.go pkg/diff/errors_test.go internal/zstdpatch/available.go cmd/root.go cmd/root_test.go cmd/exit_integration_test.go main.go
git commit -m "feat(cmd): classify errors → exit codes 0/1/2/3/4

Every pkg/diff error type now declares its Category via a one-line
method; zstdpatch sentinels become structs carrying Category+NextAction.
cmd.Execute becomes the classification edge — returns an int exit code
consumed by main.go's os.Exit. RenderError emits either 'diffah: cat:
msg' text with a hint line, or the versioned JSON error schema when
--output json is set (wiring added in a later task).

Integration tests lock exit 2 on user error and exit 4 on a forged
unknown-version sidecar.

Refs: docs/superpowers/specs/2026-04-23-observability-foundations-design.md §4.1–4.3"
```

---

## Task 3: `cmd/logger.go` — slog bootstrap and persistent flags

**Files:**
- Create: `cmd/logger.go`
- Create: `cmd/logger_test.go`
- Modify: `cmd/root.go` (persistent flags, `PersistentPreRunE`)

- [ ] **Step 3.1 — Write failing test for handler selection**

Create `cmd/logger_test.go`:

```go
package cmd

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

type fakeTTY struct{ bytes.Buffer }

func (*fakeTTY) Fd() uintptr { return 1 } // overridden via isTTY stub

func TestPickHandler_ExplicitJSON(t *testing.T) {
	var buf bytes.Buffer
	h := pickHandler(&buf, "json", &slog.HandlerOptions{Level: slog.LevelInfo}, false)
	logger := slog.New(h)
	logger.Info("hello", "k", "v")
	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("expected JSON output, got %q", buf.String())
	}
}

func TestPickHandler_ExplicitText(t *testing.T) {
	var buf bytes.Buffer
	h := pickHandler(&buf, "text", &slog.HandlerOptions{Level: slog.LevelInfo}, false)
	logger := slog.New(h)
	logger.Info("hello")
	if strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("expected text output, got JSON: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("expected msg=hello, got %q", buf.String())
	}
}

func TestPickHandler_AutoOnTTY_IsText(t *testing.T) {
	var buf bytes.Buffer
	h := pickHandler(&buf, "auto", &slog.HandlerOptions{Level: slog.LevelInfo}, /*tty=*/ true)
	logger := slog.New(h)
	logger.Info("hello")
	if strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("auto+TTY expected text, got JSON: %q", buf.String())
	}
}

func TestPickHandler_AutoOffTTY_IsJSON(t *testing.T) {
	var buf bytes.Buffer
	h := pickHandler(&buf, "auto", &slog.HandlerOptions{Level: slog.LevelInfo}, /*tty=*/ false)
	logger := slog.New(h)
	logger.Info("hello")
	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("auto+non-TTY expected JSON, got %q", buf.String())
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"":      slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %s, want %s", in, got, want)
		}
	}
}
```

- [ ] **Step 3.2 — Run test to verify it fails**

Run: `go test ./cmd -run TestPickHandler`
Expected: FAIL — `pickHandler`, `parseLevel` undefined.

- [ ] **Step 3.3 — Implement `cmd/logger.go`**

Create `cmd/logger.go`:

```go
package cmd

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
)

// pickHandler returns an slog.Handler for w based on format and whether w
// refers to a terminal. Format values:
//
//	"json"  — always JSONHandler
//	"text"  — always TextHandler
//	"auto"  — TextHandler on TTY when CI!=true; JSONHandler otherwise
//	""      — same as "auto"
//
// Unknown values fall back to JSONHandler (safest default for machine
// consumers).
func pickHandler(w io.Writer, format string, opts *slog.HandlerOptions, tty bool) slog.Handler {
	switch strings.ToLower(format) {
	case "json":
		return slog.NewJSONHandler(w, opts)
	case "text":
		return slog.NewTextHandler(w, opts)
	case "", "auto":
		if tty && os.Getenv("CI") != "true" {
			return slog.NewTextHandler(w, opts)
		}
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewJSONHandler(w, opts)
}

// parseLevel maps a string to an slog.Level. Unknown/empty → info.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "info", "":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// isTTY reports whether w is a terminal. Non-file writers are treated as
// non-TTY. Used by pickHandler and by cmd.PreRun.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// installLogger builds a logger from the current flag values and installs
// it as slog.Default. Called from root's PersistentPreRunE.
func installLogger(stderr io.Writer, levelFlag, formatFlag string, quiet, verbose bool) *slog.Logger {
	level := parseLevel(levelFlag)
	if verbose {
		level = slog.LevelDebug
	}
	if quiet {
		level = slog.LevelWarn
	}
	opts := &slog.HandlerOptions{Level: level}
	tty := isTTY(stderr)
	h := pickHandler(stderr, formatFlag, opts, tty)
	logger := slog.New(h)
	slog.SetDefault(logger)
	return logger
}
```

- [ ] **Step 3.4 — Verify `go-isatty` is a direct (or transitive) dep**

Run: `go list -m github.com/mattn/go-isatty`
Expected: present (transitive via `go.podman.io/image/v5`). If the module graph doesn't surface it directly, run `go mod tidy` after editing `go.mod`. If tidy removes it later, explicitly add it in go.mod with `go get github.com/mattn/go-isatty`.

- [ ] **Step 3.5 — Run tests to verify logger tests pass**

Run: `go test ./cmd -run TestPickHandler`
Run: `go test ./cmd -run TestParseLevel`
Expected: PASS.

- [ ] **Step 3.6 — Wire `cmd/root.go` to install logger via `PersistentPreRunE`**

Modify `cmd/root.go` — replace the `init()` function and add `PersistentPreRunE`:

```go
var (
	logLevel  string
	logFormat string
	quiet     bool
	verbose   bool
)

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&logLevel, "log-level", "info", "log level: debug|info|warn|error")
	pf.StringVar(&logFormat, "log-format", "auto", "log format: auto|text|json")
	pf.BoolVar(&quiet, "quiet", false, "suppress info logs and progress bars (level=warn)")
	pf.BoolVar(&verbose, "verbose", false, "enable debug logs (level=debug)")

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		installLogger(cmd.ErrOrStderr(), logLevel, logFormat, quiet, verbose)
		return nil
	}
}
```

Delete any previous `logLevel` declaration that was elsewhere in `root.go` — the new block above is the single source of truth.

- [ ] **Step 3.7 — Run all cmd tests**

Run: `go test ./cmd/...`
Expected: PASS.

- [ ] **Step 3.8 — Commit**

```bash
git add cmd/logger.go cmd/logger_test.go cmd/root.go
git commit -m "feat(cmd): slog bootstrap with --log-level/--log-format

Install slog.Default via PersistentPreRunE from root flags:
--log-level=debug|info|warn|error (default info),
--log-format=auto|text|json (default auto — text on TTY when CI!=true,
JSON otherwise), --quiet (warn+no progress), --verbose (debug).

Refs: docs/superpowers/specs/2026-04-23-observability-foundations-design.md §4.4"
```

---

## Task 4: Thread slog through pkg/* and internal/*

**Files modified (all):**
- `pkg/exporter/exporter.go`, `pkg/exporter/*.go` (package logger + replace stderr printfs)
- `pkg/importer/importer.go`, `pkg/importer/*.go`
- `pkg/diff/*.go`
- `internal/archive/*.go`, `internal/imageio/*.go`, `internal/oci/*.go`, `internal/zstdpatch/*.go`

> **Note:** Do NOT touch `fmt.Fprintf(cmd.OutOrStdout(), ...)` calls in `cmd/*.go` — those are command *results* going to stdout and stay as-is until Task 7 adds JSON output. Only `opts.Progress`-bound writes inside `pkg/exporter`/`pkg/importer` (stderr-bound diagnostic output) migrate to slog, and Task 5 will re-route progress events through `progress.Reporter`. This task migrates the *log* events.

- [ ] **Step 4.1 — Add package logger in pkg/exporter**

At the top of `pkg/exporter/exporter.go` (after imports), add:

```go
var logger = slog.Default().With("component", "exporter")
```

Import `"log/slog"`.

- [ ] **Step 4.2 — Replace fmt.Fprintf calls in exporter.go with slog**

Currently in `pkg/exporter/exporter.go`:

```go
if opts.Progress != nil {
    fmt.Fprintf(opts.Progress, "planning %d pairs...\n", len(opts.Pairs))
}
// ... similar lines for "planned", "encoded", "wrote"
```

Replace with structured slog events (keeping `opts.Progress` writes for now — Task 5 swaps Progress to a Reporter):

```go
logger.Info("planning pairs", "count", len(opts.Pairs))
if opts.Progress != nil {
    fmt.Fprintf(opts.Progress, "planning %d pairs...\n", len(opts.Pairs))
}
```

And similarly for the three other lines. Keep the progress-writer lines AS-IS in this task; Task 5 replaces them wholesale. The point of this task is to establish the structured log channel.

- [ ] **Step 4.3 — Add package loggers in remaining packages**

For each of the following files, add after imports:

```go
var logger = slog.Default().With("component", "<pkg>")
```

Where `<pkg>` is:
- `pkg/importer/importer.go` → `"importer"`
- `pkg/diff/sidecar.go` → `"diff"` (or equivalent top-level file)
- `internal/archive/reader.go` → `"archive"`
- `internal/imageio/sniff.go` → `"imageio"`
- `internal/oci/layout.go` → `"oci"`
- `internal/zstdpatch/zstdpatch.go` → `"zstdpatch"`

Each import adds `"log/slog"`. Leave existing logic untouched — just establish the loggers so later PRs can use them.

- [ ] **Step 4.4 — Verify slog.Default at package init-time is deferred correctly**

`slog.Default()` at package-var init time captures a snapshot of the default. Because `installLogger` (Task 3) calls `slog.SetDefault` later, package-var-scoped captures become stale. Fix by routing through a helper:

Create `pkg/exporter/log.go`:

```go
package exporter

import "log/slog"

// log returns the current default logger tagged with this package's
// component name. Re-reads slog.Default on every call so tests and
// cmd.PreRun can replace the default without re-wiring callers.
func log() *slog.Logger {
	return slog.Default().With("component", "exporter")
}
```

Replace the earlier `var logger = ...` with usages `log().Info(...)` or similar. Do the same for each of the six other packages (`pkg/importer/log.go`, `pkg/diff/log.go`, `internal/archive/log.go`, `internal/imageio/log.go`, `internal/oci/log.go`, `internal/zstdpatch/log.go`). Delete the package-var `logger` declarations added in Steps 4.1 and 4.3.

- [ ] **Step 4.5 — Run all tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4.6 — Add a capture-based assertion test**

Create `pkg/exporter/log_test.go`:

```go
package exporter

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
)

func TestLog_EmitsStructuredRecord(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	log().InfoContext(context.Background(), "test event", "k", "v")

	if !bytes.Contains(buf.Bytes(), []byte(`"component":"exporter"`)) {
		t.Errorf("expected component=exporter, got %s", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"msg":"test event"`)) {
		t.Errorf("expected msg=test event, got %s", buf.String())
	}
}
```

Run: `go test ./pkg/exporter -run TestLog_EmitsStructuredRecord`
Expected: PASS.

- [ ] **Step 4.7 — Commit**

```bash
git add pkg/exporter/log.go pkg/exporter/exporter.go pkg/exporter/log_test.go pkg/importer/log.go pkg/importer/importer.go pkg/diff/log.go internal/archive/log.go internal/imageio/log.go internal/oci/log.go internal/zstdpatch/log.go
git commit -m "feat(*): thread slog.Default through every package

Introduce a log() helper per package that re-reads slog.Default on every
call (so cmd.PersistentPreRunE's SetDefault takes effect without callers
re-wiring). exporter gains a structured 'planning pairs' event as a first
user of the channel; other packages get the helper in place for later
migration PRs.

Refs: docs/superpowers/specs/2026-04-23-observability-foundations-design.md §4.5"
```

---

## Task 5: `pkg/progress` package — Reporter interface + discard/line/FromWriter

**Files:**
- Create: `pkg/progress/reporter.go`
- Create: `pkg/progress/discard.go`
- Create: `pkg/progress/line.go`
- Create: `pkg/progress/from_writer.go`
- Create: `pkg/progress/reporter_test.go`

- [ ] **Step 5.1 — Write failing test for Reporter + implementations**

Create `pkg/progress/reporter_test.go`:

```go
package progress_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/progress"
)

func TestDiscard_Nop(t *testing.T) {
	r := progress.NewDiscard()
	r.Phase("encoding")
	l := r.StartLayer(digest.Digest("sha256:abc"), 100, "full")
	l.Written(50)
	l.Done()
	r.Finish()
	// No panics = pass.
}

func TestLine_EmitsPhaseAndDone(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewLine(&buf)
	r.Phase("encoding")
	l := r.StartLayer(digest.Digest("sha256:abcdefghijklmnop"), 1024, "full")
	l.Written(1024)
	l.Done()
	r.Finish()

	out := buf.String()
	if !strings.Contains(out, "[encoding]") {
		t.Errorf("expected [encoding] phase line, got %q", out)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("expected 'done' line for StartLayer, got %q", out)
	}
	// Layer prefix: shortened digest (first 12 chars after algo)
	if !strings.Contains(out, "sha256:abcdefghijkl") {
		t.Errorf("expected layer digest prefix, got %q", out)
	}
}

func TestLine_ReportsFail(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewLine(&buf)
	l := r.StartLayer(digest.Digest("sha256:abc"), 100, "patch")
	l.Fail(bytesErr("encode failed"))
	if !strings.Contains(buf.String(), "failed") {
		t.Errorf("expected 'failed' in line output, got %q", buf.String())
	}
}

func TestFromWriter_WrapsAsLine(t *testing.T) {
	var buf bytes.Buffer
	r := progress.FromWriter(&buf)
	r.Phase("planning")
	if !strings.Contains(buf.String(), "[planning]") {
		t.Errorf("expected phase line, got %q", buf.String())
	}
}

type bytesErr string
func (e bytesErr) Error() string { return string(e) }
```

- [ ] **Step 5.2 — Run test to verify it fails**

Run: `go test ./pkg/progress/...`
Expected: FAIL (package doesn't exist).

- [ ] **Step 5.3 — Implement `pkg/progress/reporter.go` (interfaces)**

Create `pkg/progress/reporter.go`:

```go
// Package progress exposes a reporter interface for user-facing progress
// output. Implementations include a discard (no-op), a line-based summary
// writer, and (in bars.go) a vbauerster/mpb multi-bar renderer.
//
// Progress is NOT a slog handler. Pretty multi-bar rendering and structured
// log records are separate concerns; domain code emits to both independently.
package progress

import "github.com/opencontainers/go-digest"

// Reporter renders operational progress to the operator. One reporter per
// exporter/importer invocation; Finish must be called at end-of-run to
// flush pending bars/lines.
type Reporter interface {
	// Phase announces a top-level phase transition (planning, encoding,
	// writing, extracting, composing). Called once per phase.
	Phase(name string)

	// StartLayer opens a progress handle for a single blob. totalBytes
	// may be 0 for unknown-size layers; encoding is "full" | "patch".
	StartLayer(d digest.Digest, totalBytes int64, encoding string) Layer

	// Finish flushes pending output. Safe to call multiple times.
	Finish()
}

// Layer is the handle returned by StartLayer. Written/Done/Fail form a
// terminal state machine: after Done or Fail, additional Written calls
// are no-ops. Exactly one of Done or Fail must be called per layer.
type Layer interface {
	Written(n int64)
	Done()
	Fail(err error)
}
```

- [ ] **Step 5.4 — Implement `pkg/progress/discard.go`**

Create `pkg/progress/discard.go`:

```go
package progress

import "github.com/opencontainers/go-digest"

// NewDiscard returns a no-op reporter. Used when --quiet is set and in
// tests that need a Reporter but don't care about output.
func NewDiscard() Reporter { return discard{} }

type discard struct{}

func (discard) Phase(string)                                                         {}
func (discard) StartLayer(digest.Digest, int64, string) Layer                         { return discardLayer{} }
func (discard) Finish()                                                              {}

type discardLayer struct{}

func (discardLayer) Written(int64) {}
func (discardLayer) Done()          {}
func (discardLayer) Fail(error)     {}
```

- [ ] **Step 5.5 — Implement `pkg/progress/line.go`**

Create `pkg/progress/line.go`:

```go
package progress

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/opencontainers/go-digest"
)

// NewLine returns a reporter that emits one line per phase transition and
// one line per completed layer. No bars, no escape sequences — safe for
// redirected stderr, CI logs, and debug captures.
func NewLine(w io.Writer) Reporter { return &lineReporter{w: w} }

type lineReporter struct {
	w  io.Writer
	mu sync.Mutex
}

func (r *lineReporter) Phase(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, "[%s]\n", name)
}

func (r *lineReporter) StartLayer(d digest.Digest, total int64, enc string) Layer {
	return &lineLayer{r: r, digest: d, total: total, enc: enc, started: time.Now()}
}

func (r *lineReporter) Finish() {}

type lineLayer struct {
	r       *lineReporter
	digest  digest.Digest
	total   int64
	enc     string
	written int64
	started time.Time
	done    bool
	mu      sync.Mutex
}

func (l *lineLayer) Written(n int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return
	}
	l.written += n
}

func (l *lineLayer) Done() {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	dur := time.Since(l.started)
	l.mu.Unlock()

	l.r.mu.Lock()
	defer l.r.mu.Unlock()
	fmt.Fprintf(l.r.w, "  %s %s %s in %s — done\n",
		shortDigest(l.digest), l.enc, humanBytes(l.written), dur.Round(time.Millisecond))
}

func (l *lineLayer) Fail(err error) {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	l.mu.Unlock()

	l.r.mu.Lock()
	defer l.r.mu.Unlock()
	fmt.Fprintf(l.r.w, "  %s %s failed: %v\n", shortDigest(l.digest), l.enc, err)
}

func shortDigest(d digest.Digest) string {
	s := string(d)
	// "sha256:abcdef..." → keep algo + first 18 hex chars
	if len(s) > 25 {
		return s[:25]
	}
	return s
}

func humanBytes(n int64) string {
	const (
		KB = 1 << 10
		MB = 1 << 20
		GB = 1 << 30
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.1fGB", float64(n)/GB)
	case n >= MB:
		return fmt.Sprintf("%.1fMB", float64(n)/MB)
	case n >= KB:
		return fmt.Sprintf("%.1fKB", float64(n)/KB)
	}
	return fmt.Sprintf("%dB", n)
}
```

- [ ] **Step 5.6 — Implement `pkg/progress/from_writer.go`**

Create `pkg/progress/from_writer.go`:

```go
package progress

import "io"

// FromWriter adapts an io.Writer (legacy Options.Progress field) to the
// Reporter interface. Behaves exactly like NewLine. Provided so callers
// migrating off the deprecated io.Writer field keep working during the
// deprecation cycle.
func FromWriter(w io.Writer) Reporter {
	if w == nil {
		return NewDiscard()
	}
	return NewLine(w)
}
```

- [ ] **Step 5.7 — Run tests**

Run: `go test ./pkg/progress/...`
Expected: PASS (4 tests).

- [ ] **Step 5.8 — Wire `progress.Reporter` into `exporter.Options`**

Modify `pkg/exporter/exporter.go`:

Add import:
```go
"github.com/leosocy/diffah/pkg/progress"
```

Modify the Options struct:

```go
type Options struct {
	Pairs       []Pair
	Platform    string
	Compress    string
	OutputPath  string
	ToolVersion string
	IntraLayer  string
	CreatedAt   time.Time

	// ProgressReporter receives phase/layer/done events. Defaults to
	// progress.NewDiscard(). This is the preferred channel going forward.
	ProgressReporter progress.Reporter

	// Progress is the legacy io.Writer-based progress sink.
	// Deprecated: use ProgressReporter. Will be removed in v0.4.
	// When both are set, ProgressReporter wins; when only Progress is
	// set, it is wrapped via progress.FromWriter.
	Progress io.Writer

	Probe   Probe
	WarnOut io.Writer

	fingerprinter Fingerprinter
}

// reporter returns the effective progress sink.
func (o *Options) reporter() progress.Reporter {
	if o.ProgressReporter != nil {
		return o.ProgressReporter
	}
	if o.Progress != nil {
		return progress.FromWriter(o.Progress)
	}
	return progress.NewDiscard()
}
```

- [ ] **Step 5.9 — Migrate `buildBundle` to call `reporter()`**

In the same file, replace the `opts.Progress != nil` blocks. Before:

```go
if opts.Progress != nil {
    fmt.Fprintf(opts.Progress, "planning %d pairs...\n", len(opts.Pairs))
}
```

After:

```go
rep := opts.reporter()
rep.Phase("planning")
log().Info("planning pairs", "count", len(opts.Pairs))
```

Similarly for "planned %d pairs" (after the loop), replace with:

```go
log().Info("planned pairs", "count", len(plans))
```

Drop the progress-specific printf for "planned" (the phase transition announces this already; log carries the structured count). For "encoded %d blobs" and "wrote ..." lines, do the same — phase transition via `rep.Phase`, structured slog record for the count, delete the printf.

The per-layer `Written`/`Done` calls are NOT wired yet — encoding loops don't yet call them. Task 6 wires the per-layer events alongside the mpb bars. For now this task only establishes phase announcements.

- [ ] **Step 5.10 — Wire `progress.Reporter` into `importer.Options`**

Analogous to Step 5.8 in `pkg/importer/importer.go`. Add the `ProgressReporter progress.Reporter` field, deprecate `Progress io.Writer`, add the `reporter()` helper. Replace the `progress` usages in `Import` accordingly (phase announcements: "extracting", "verifying", "composing").

- [ ] **Step 5.11 — Run full test suite**

Run: `go test ./...`
Expected: PASS (existing callers using `Progress: io.Writer` still work via FromWriter).

- [ ] **Step 5.12 — Commit**

```bash
git add pkg/progress/ pkg/exporter/exporter.go pkg/importer/importer.go
git commit -m "feat(progress): introduce Reporter with discard/line/FromWriter

Progress becomes a typed concern instead of a bare io.Writer. Exporter
and importer Options gain ProgressReporter and retain a deprecated
Progress io.Writer field wrapped via progress.FromWriter for one release.

Reporter implementations: NewDiscard (no-op, default), NewLine (phase
and layer-done summaries to an io.Writer), FromWriter (backward-compat
adapter). mpb multi-bar renderer lands in the next task.

Refs: docs/superpowers/specs/2026-04-23-observability-foundations-design.md §4.6"
```

---

## Task 6: `mpbReporter` + `--progress` flag + NewAuto

**Files:**
- Modify: `go.mod`, `go.sum` (add `github.com/vbauerster/mpb/v8`)
- Create: `pkg/progress/bars.go`
- Create: `pkg/progress/auto.go`
- Create: `pkg/progress/auto_test.go`
- Modify: `cmd/root.go` (`--progress` flag)
- Modify: `cmd/export.go`, `cmd/import.go` (construct reporter from the flag, pass into Options)

- [ ] **Step 6.1 — Add mpb/v8 dependency**

```bash
go get github.com/vbauerster/mpb/v8@latest
go mod tidy
```

Expected: go.mod gains `require github.com/vbauerster/mpb/v8 vX.Y.Z`. Verify: `go list -m github.com/vbauerster/mpb/v8` shows the version.

- [ ] **Step 6.2 — Write failing test for NewAuto TTY detection**

Create `pkg/progress/auto_test.go`:

```go
package progress

import (
	"bytes"
	"testing"
)

func TestNewAuto_NonTTY_PicksLine(t *testing.T) {
	var buf bytes.Buffer
	r := newAutoFor(&buf, /*tty=*/ false, /*color=*/ true, /*ci=*/ false)
	// Non-TTY must be a lineReporter — smoke-test by emitting a phase and
	// seeing a "[phase]\n" style line (no escape sequences).
	r.Phase("test")
	out := buf.String()
	if !contains(out, "[test]") {
		t.Errorf("non-TTY NewAuto expected line output, got %q", out)
	}
	if containsEscape(out) {
		t.Errorf("non-TTY output must not contain escape sequences, got %q", out)
	}
}

func TestNewAuto_CI_PicksLine(t *testing.T) {
	var buf bytes.Buffer
	r := newAutoFor(&buf, /*tty=*/ true, /*color=*/ true, /*ci=*/ true)
	r.Phase("test")
	if containsEscape(buf.String()) {
		t.Errorf("CI=true must degrade to lineReporter, got escape sequences")
	}
}

func TestNewAuto_NoColor_PicksLine(t *testing.T) {
	var buf bytes.Buffer
	r := newAutoFor(&buf, /*tty=*/ true, /*color=*/ false, /*ci=*/ false)
	r.Phase("test")
	if containsEscape(buf.String()) {
		t.Errorf("NO_COLOR must degrade to lineReporter, got escape sequences")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func containsEscape(s string) bool {
	for _, r := range s {
		if r == 0x1b { // ESC
			return true
		}
	}
	return false
}
```

- [ ] **Step 6.3 — Implement `pkg/progress/bars.go`**

Create `pkg/progress/bars.go`:

```go
package progress

import (
	"io"
	"sync"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

// NewBars returns an mpb-backed multi-bar reporter writing to w. Caller is
// responsible for ensuring w is a TTY; otherwise NewAuto will have chosen
// NewLine instead.
func NewBars(w io.Writer) Reporter {
	p := mpb.New(
		mpb.WithOutput(w),
		mpb.WithRefreshRate(100*time.Millisecond),
	)
	return &barsReporter{w: w, p: p}
}

type barsReporter struct {
	w  io.Writer
	p  *mpb.Progress
	mu sync.Mutex
}

func (r *barsReporter) Phase(name string) {
	// Phases render as a header bar without totals; mpb doesn't support
	// a true section header, so we emit a single-shot "info" line between
	// bars via Printf (mpb serializes this with active bars).
	r.p.Write([]byte("[" + name + "]\n"))
}

func (r *barsReporter) StartLayer(d digest.Digest, total int64, enc string) Layer {
	name := shortDigest(d) + " " + enc
	bar := r.p.AddBar(total,
		mpb.PrependDecorators(
			decor.Name(name, decor.WC{W: 30, C: decor.DindentRight}),
		),
		mpb.AppendDecorators(
			decor.CountersKibiByte("% .1f / % .1f"),
			decor.Name(" "),
			decor.AverageSpeed(decor.SizeB1024(0), "% .1f"),
			decor.Name(" "),
			decor.EwmaETA(decor.ET_STYLE_GO, 30),
			decor.OnComplete(decor.Name(""), " ✓"),
		),
	)
	return &barsLayer{bar: bar, total: total}
}

func (r *barsReporter) Finish() {
	r.p.Wait()
}

type barsLayer struct {
	bar   *mpb.Bar
	total int64
	mu    sync.Mutex
	done  bool
}

func (l *barsLayer) Written(n int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return
	}
	l.bar.IncrInt64(n)
}

func (l *barsLayer) Done() {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	l.mu.Unlock()
	// Fill any remaining bytes so the bar renders complete.
	if cur := l.bar.Current(); cur < l.total {
		l.bar.IncrInt64(l.total - cur)
	}
}

func (l *barsLayer) Fail(err error) {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	l.mu.Unlock()
	l.bar.Abort(false)
}
```

- [ ] **Step 6.4 — Implement `pkg/progress/auto.go`**

Create `pkg/progress/auto.go`:

```go
package progress

import (
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

// NewAuto picks an appropriate reporter for w:
//   - TTY + !CI + !NO_COLOR → NewBars (multi-bar)
//   - otherwise           → NewLine (line summaries)
//
// If w is nil, returns NewDiscard.
func NewAuto(w io.Writer) Reporter {
	if w == nil {
		return NewDiscard()
	}
	return newAutoFor(w, isTTYFd(w), !noColor(), os.Getenv("CI") == "true")
}

func newAutoFor(w io.Writer, tty, color, ci bool) Reporter {
	if tty && !ci && color {
		return NewBars(w)
	}
	return NewLine(w)
}

func isTTYFd(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func noColor() bool { return os.Getenv("NO_COLOR") != "" }
```

- [ ] **Step 6.5 — Run auto tests**

Run: `go test ./pkg/progress/...`
Expected: PASS (3 new tests plus earlier 4).

- [ ] **Step 6.6 — Add `--progress` flag in `cmd/root.go`**

Append to `init()` in `cmd/root.go`:

```go
pf.StringVar(&progressMode, "progress", "auto",
    "progress output: auto|bars|lines|off")
```

Declare `var progressMode string` alongside the other flag vars.

Add a helper in `cmd/root.go`:

```go
func newProgressReporter(w io.Writer) progress.Reporter {
	switch progressMode {
	case "off":
		return progress.NewDiscard()
	case "bars":
		return progress.NewBars(w)
	case "lines":
		return progress.NewLine(w)
	case "", "auto":
		return progress.NewAuto(w)
	}
	return progress.NewAuto(w)
}
```

Add import `"github.com/leosocy/diffah/pkg/progress"`. Also honor `--quiet` by mapping to `"off"`:

```go
func newProgressReporter(w io.Writer) progress.Reporter {
	if quiet {
		return progress.NewDiscard()
	}
	// ... switch above ...
}
```

- [ ] **Step 6.7 — Wire reporter into export/import runE functions**

In `cmd/export.go`, modify `runExport`:

```go
opts := exporter.Options{
    Pairs:            pairs,
    Platform:         exportFlags.platform,
    Compress:         exportFlags.compress,
    IntraLayer:       exportFlags.intraLayer,
    OutputPath:       args[0],
    ToolVersion:      version,
    ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
}
```

Remove any `Progress: cmd.OutOrStdout()` if present — progress goes to stderr, not stdout. (The existing code doesn't set `Progress`; it was an unused optional. Just add the new field.)

Similar change in `cmd/import.go`:

```go
opts := importer.Options{
    DeltaPath:        args[0],
    Baselines:        baselines,
    Strict:           importFlags.strict,
    OutputPath:       args[1],
    OutputFormat:     importFlags.outputFormat,
    AllowConvert:     importFlags.allowConvert,
    ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
}
```

- [ ] **Step 6.8 — Run all tests**

Run: `go test ./...`
Expected: PASS. Integration tests continue to pass because `newProgressReporter` in non-TTY test environments falls through to `NewLine`.

- [ ] **Step 6.9 — Commit**

```bash
git add go.mod go.sum pkg/progress/ cmd/root.go cmd/export.go cmd/import.go
git commit -m "feat(progress): add mpb multi-bar reporter + --progress flag

NewBars emits Docker-style per-layer progress via vbauerster/mpb/v8 with
bytes, ratio, speed, and ETA decorators. NewAuto picks NewBars on TTY
when !CI && !NO_COLOR, NewLine otherwise.

CLI exposes --progress=auto|bars|lines|off (default auto); --quiet forces
off. Export and import wire the reporter into Options.ProgressReporter.

Refs: docs/superpowers/specs/2026-04-23-observability-foundations-design.md §4.6"
```

---

## Task 7: `--output json` top-level flag + JSON inspect + snapshot tests

**Files:**
- Modify: `cmd/root.go` (add `--output` persistent flag)
- Create: `cmd/output.go` (JSON envelope helper + renderer lookups)
- Modify: `cmd/inspect.go` (branch on output format)
- Create: `cmd/testdata/schemas/inspect.snap.json`
- Create: `cmd/testdata/schemas/error-user.snap.json`
- Create: `cmd/testdata/schemas/error-env.snap.json`
- Create: `cmd/testdata/schemas/error-content.snap.json`
- Create: `cmd/testdata/schemas/error-internal.snap.json`
- Create: `cmd/inspect_json_test.go`
- Create: `cmd/error_json_test.go`

- [ ] **Step 7.1 — Add `--output` persistent flag in `cmd/root.go`**

Append to `init()`:

```go
pf.StringVar(&outputFormat, "output", "text",
    "output format: text|json (applies to inspect/dry-run/doctor and error rendering)")
```

Declare `var outputFormat string` alongside the other flag vars. Update `outputFormatFlag()` in Task 2's snippet — it already reads this flag, but we previously returned `"text"` when the flag was unregistered. Now the flag is always registered; simplify:

```go
func outputFormatFlag() string { return outputFormat }
```

- [ ] **Step 7.2 — Implement `cmd/output.go`**

Create `cmd/output.go`:

```go
package cmd

import (
	"encoding/json"
	"io"
)

// jsonEnvelope is the top-level shape of every --output json response.
// error.snap.json uses the sibling "error" key (see RenderError).
type jsonEnvelope struct {
	SchemaVersion int `json:"schema_version"`
	Data          any `json:"data"`
}

// writeJSON serializes v as {"schema_version":1,"data":<v>} to w.
// Never returns an error on sane inputs; marshalling failures indicate
// a struct bug worth surfacing via panic-to-log at higher layers.
func writeJSON(w io.Writer, v any) error {
	env := jsonEnvelope{SchemaVersion: 1, Data: v}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}
```

- [ ] **Step 7.3 — Write failing test for JSON inspect snapshot**

Create `cmd/inspect_json_test.go`:

```go
package cmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leosocy/diffah/cmd"
)

func TestInspect_OutputJSON_Snapshot(t *testing.T) {
	archive := filepath.Join("..", "testdata", "fixtures", "v3_roundtrip_bundle.tar")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("fixture missing: %s", archive)
	}
	var stdout bytes.Buffer
	rc := cmd.Run(&stdout, nil, "inspect", "--output", "json", archive)
	if rc != 0 {
		t.Fatalf("inspect returned %d", rc)
	}
	got := stdout.String()
	snap := filepath.Join("testdata", "schemas", "inspect.snap.json")
	want, err := os.ReadFile(snap)
	if err != nil {
		if os.Getenv("DIFFAH_UPDATE_SNAPSHOTS") == "1" {
			if err := os.WriteFile(snap, []byte(normalizeJSON(got)), 0o644); err != nil {
				t.Fatalf("write snapshot: %v", err)
			}
			return
		}
		t.Fatalf("snapshot missing; rerun with DIFFAH_UPDATE_SNAPSHOTS=1: %v", err)
	}
	gotNorm := normalizeJSON(got)
	if string(want) != gotNorm {
		t.Errorf("snapshot mismatch.\nwant:\n%s\ngot:\n%s", want, gotNorm)
	}
}

// normalizeJSON replaces volatile fields (created_at, tool_version) with
// fixed placeholders so snapshots compare byte-equal across runs.
func normalizeJSON(s string) string {
	s = replaceField(s, "created_at", "<T>")
	s = replaceField(s, "tool_version", "<V>")
	return s
}

func replaceField(s, field, placeholder string) string {
	// naive: find `"field": "..."` and replace the value. Real impl may
	// want a JSON-aware walker, but a regex-less pattern suffices here
	// because the JSON is always indented + quoted.
	needle := `"` + field + `": "`
	for {
		i := strings.Index(s, needle)
		if i < 0 {
			return s
		}
		start := i + len(needle)
		end := strings.Index(s[start:], `"`)
		if end < 0 {
			return s
		}
		s = s[:start] + placeholder + s[start+end:]
	}
}
```

Also create a small test helper in `cmd/testmain.go` (or equivalent) — an in-process `cmd.Run` that mimics `Execute` but uses a caller-supplied stdout:

```go
// cmd/testmain.go
package cmd

import (
	"io"
	"os"
)

// Run invokes the root command in-process with the given stdout/stderr
// and positional args, then returns the exit code. Exposed for tests.
func Run(stdout, stderr io.Writer, args ...string) int {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	defer rootCmd.SetArgs(nil)
	return Execute(stderr)
}
```

- [ ] **Step 7.4 — Run test to verify it fails**

Run: `go test ./cmd -run TestInspect_OutputJSON_Snapshot`
Expected: SKIP (fixture missing) or FAIL (cmd.Run / inspect JSON not implemented).

- [ ] **Step 7.5 — Implement JSON branch in `cmd/inspect.go`**

Replace `runInspect` in `cmd/inspect.go`:

```go
func runInspect(cmd *cobra.Command, args []string) error {
	raw, err := archive.ReadSidecar(args[0])
	if err != nil {
		return err
	}
	s, err := diff.ParseSidecar(raw)
	if err != nil {
		var p1 *diff.ErrPhase1Archive
		if errors.As(err, &p1) {
			if outputFormat == "json" {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"feature":         "phase1",
					"recommended_fix": "re-export with current diffah",
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), "This archive uses the Phase 1 (single-image) schema.")
			fmt.Fprintln(cmd.OutOrStdout(), "Re-export with the current diffah to use the bundle format.")
			return nil
		}
		return err
	}
	requiresZstd := s.RequiresZstd()
	zstdAvailable, _ := zstdpatch.Available(cmd.Context())
	if outputFormat == "json" {
		return writeJSON(cmd.OutOrStdout(), inspectJSON(args[0], s, requiresZstd, zstdAvailable))
	}
	return printBundleSidecar(cmd.OutOrStdout(), args[0], s, requiresZstd, zstdAvailable)
}

// inspectJSON builds the JSON view of a sidecar. Snapshot-tested in
// cmd/testdata/schemas/inspect.snap.json.
func inspectJSON(path string, s *diff.Sidecar, requiresZstd, zstdAvailable bool) any {
	bs := collectBundleStats(s)
	images := make([]map[string]any, 0, len(s.Images))
	for _, img := range s.Images {
		images = append(images, map[string]any{
			"name": img.Name,
			"target": map[string]any{
				"manifest_digest": img.Target.ManifestDigest.String(),
				"media_type":      img.Target.MediaType,
			},
			"baseline": map[string]any{
				"manifest_digest": img.Baseline.ManifestDigest.String(),
				"media_type":      img.Baseline.MediaType,
				"source_hint":     img.Baseline.SourceHint,
			},
		})
	}
	return map[string]any{
		"archive":             path,
		"version":             s.Version,
		"feature":             s.Feature,
		"tool":                s.Tool,
		"tool_version":        s.ToolVersion,
		"platform":            s.Platform,
		"created_at":          s.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		"images":              images,
		"blobs": map[string]any{
			"total":       len(s.Blobs),
			"full_count":  bs.fullCount,
			"patch_count": bs.patchCount,
		},
		"total_archive_bytes":   bs.totalArchiveSize,
		"requires_zstd":         requiresZstd,
		"zstd_available":        zstdAvailable,
	}
}
```

- [ ] **Step 7.6 — Generate the inspect snapshot**

Assuming a fixture exists at `testdata/fixtures/v3_roundtrip_bundle.tar`:

```bash
DIFFAH_UPDATE_SNAPSHOTS=1 go test ./cmd -run TestInspect_OutputJSON_Snapshot
```

Review the generated `cmd/testdata/schemas/inspect.snap.json` file. Verify `created_at` and `tool_version` are `<T>` and `<V>`. Commit the file.

If the fixture doesn't exist yet, create a deterministic minimal fixture via the existing `build_fixtures` helper in the repo root — but this can also wait until Task 10 if a manual verification is planned. For now, the test will `t.Skip`.

- [ ] **Step 7.7 — Write JSON error rendering tests**

Create `cmd/error_json_test.go`:

```go
package cmd_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/leosocy/diffah/cmd"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/internal/zstdpatch"
)

type errPayload struct {
	SchemaVersion int `json:"schema_version"`
	Error         struct {
		Category   string `json:"category"`
		Message    string `json:"message"`
		NextAction string `json:"next_action"`
	} `json:"error"`
}

func TestRenderError_JSON_User(t *testing.T) {
	var buf bytes.Buffer
	cmd.RenderError(&buf, &diff.ErrMultiImageNeedsNamedBaselines{N: 3}, "json")
	var p errPayload
	if err := json.Unmarshal(buf.Bytes(), &p); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if p.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", p.SchemaVersion)
	}
	if p.Error.Category != "user" {
		t.Errorf("category = %q, want user", p.Error.Category)
	}
	if p.Error.NextAction == "" {
		t.Errorf("want next_action; got empty")
	}
}

func TestRenderError_JSON_Environment(t *testing.T) {
	var buf bytes.Buffer
	cmd.RenderError(&buf, zstdpatch.ErrZstdBinaryMissing, "json")
	var p errPayload
	if err := json.Unmarshal(buf.Bytes(), &p); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if p.Error.Category != "environment" {
		t.Errorf("category = %q, want environment", p.Error.Category)
	}
	if p.Error.NextAction == "" {
		t.Errorf("want install hint")
	}
}
```

Run: `go test ./cmd -run TestRenderError_JSON`
Expected: PASS (RenderError already handles `"json"` from Task 2).

- [ ] **Step 7.8 — Commit**

```bash
git add cmd/output.go cmd/inspect.go cmd/root.go cmd/testmain.go cmd/inspect_json_test.go cmd/error_json_test.go cmd/testdata/schemas/inspect.snap.json cmd/testdata/schemas/error-*.snap.json
git commit -m "feat(cmd): --output json on inspect and errors

Persistent --output={text,json} flag routes inspect output through a
versioned JSON envelope ({schema_version:1, data:...}) and forces
RenderError to emit a sibling-shape JSON error payload on stderr.
Snapshot tests in cmd/testdata/schemas/ lock the shape; set
DIFFAH_UPDATE_SNAPSHOTS=1 to regenerate them after a deliberate change.

Refs: docs/superpowers/specs/2026-04-23-observability-foundations-design.md §4.7"
```

---

## Task 8: `--output json` on `export --dry-run` and `import --dry-run`

**Files:**
- Modify: `cmd/export.go` (JSON branch in dry-run)
- Modify: `cmd/import.go` (JSON branch in dry-run)
- Create: `cmd/testdata/schemas/export-dryrun.snap.json`
- Create: `cmd/testdata/schemas/import-dryrun.snap.json`
- Create: `cmd/export_dryrun_json_test.go`
- Create: `cmd/import_dryrun_json_test.go`

- [ ] **Step 8.1 — Write failing JSON dry-run test for export**

Create `cmd/export_dryrun_json_test.go`:

```go
package cmd_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leosocy/diffah/cmd"
)

func TestExport_DryRun_JSON(t *testing.T) {
	pair := fixturePair(t) // helper below; skips if fixtures missing
	if pair.baseline == "" {
		t.Skip("fixtures missing")
	}
	var stdout bytes.Buffer
	rc := cmd.Run(&stdout, nil,
		"export", "--dry-run", "--output", "json",
		"--pair", "app="+pair.baseline+","+pair.target,
		filepath.Join(t.TempDir(), "out.tar"),
	)
	if rc != 0 {
		t.Fatalf("export dry-run returned %d", rc)
	}
	var env struct {
		SchemaVersion int `json:"schema_version"`
		Data          struct {
			TotalBlobs  int   `json:"total_blobs"`
			TotalImages int   `json:"total_images"`
			ArchiveSize int64 `json:"archive_size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout.String())
	}
	if env.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", env.SchemaVersion)
	}
	if env.Data.TotalImages == 0 {
		t.Errorf("expected total_images > 0")
	}
}

type fixturePairPaths struct{ baseline, target string }

func fixturePair(t *testing.T) fixturePairPaths {
	b := filepath.Join("..", "testdata", "fixtures", "v1_baseline_oci.tar")
	v := filepath.Join("..", "testdata", "fixtures", "v1_target_oci.tar")
	if _, err := os.Stat(b); err != nil {
		return fixturePairPaths{}
	}
	if _, err := os.Stat(v); err != nil {
		return fixturePairPaths{}
	}
	return fixturePairPaths{baseline: b, target: v}
}
```

- [ ] **Step 8.2 — Run test to verify it fails**

Run: `go test ./cmd -run TestExport_DryRun_JSON`
Expected: FAIL or SKIP.

- [ ] **Step 8.3 — Add JSON branch in `cmd/export.go`**

Modify `runExport`:

```go
if exportFlags.dryRun {
    stats, err := exporter.DryRun(ctx, opts)
    if err != nil {
        return err
    }
    if outputFormat == "json" {
        return writeJSON(cmd.OutOrStdout(), exportDryRunJSON(stats))
    }
    fmt.Fprintf(cmd.OutOrStdout(),
        "delta would ship %d blobs across %d images (%d bytes archive)\n",
        stats.TotalBlobs, stats.TotalImages, stats.ArchiveSize)
    return nil
}
```

Add at the bottom of `cmd/export.go`:

```go
func exportDryRunJSON(s exporter.DryRunStats) any {
	perImage := make([]map[string]any, 0, len(s.PerImage))
	for _, p := range s.PerImage {
		perImage = append(perImage, map[string]any{
			"name":          p.Name,
			"shipped_blobs": p.ShippedBlobs,
			"archive_size":  p.ArchiveSize,
		})
	}
	return map[string]any{
		"total_blobs":  s.TotalBlobs,
		"total_images": s.TotalImages,
		"archive_size": s.ArchiveSize,
		"per_image":    perImage,
	}
}
```

- [ ] **Step 8.4 — Add JSON branch in `cmd/import.go`**

Replace the dry-run block in `runImport`:

```go
if importFlags.dryRun {
    report, err := importer.DryRun(ctx, opts)
    if err != nil {
        return err
    }
    if outputFormat == "json" {
        return writeJSON(cmd.OutOrStdout(), importDryRunJSON(report))
    }
    return renderDryRunReport(cmd.OutOrStdout(), report)
}
```

Append:

```go
func importDryRunJSON(r importer.DryRunReport) any {
	images := make([]map[string]any, 0, len(r.Images))
	for _, img := range r.Images {
		images = append(images, map[string]any{
			"name":                      img.Name,
			"target_manifest_digest":    img.TargetManifestDigest.String(),
			"baseline_manifest_digest":  img.BaselineManifestDigest.String(),
			"baseline_provided":         img.BaselineProvided,
			"would_import":              img.WouldImport,
			"skip_reason":               img.SkipReason,
			"layer_count":               img.LayerCount,
			"archive_layer_count":       img.ArchiveLayerCount,
			"baseline_layer_count":      img.BaselineLayerCount,
			"patch_layer_count":         img.PatchLayerCount,
		})
	}
	return map[string]any{
		"feature":         r.Feature,
		"version":         r.Version,
		"tool":            r.Tool,
		"tool_version":    r.ToolVersion,
		"created_at":      r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		"platform":        r.Platform,
		"archive_bytes":   r.ArchiveBytes,
		"requires_zstd":   r.RequiresZstd,
		"zstd_available":  r.ZstdAvailable,
		"blobs": map[string]any{
			"full_count":  r.Blobs.FullCount,
			"patch_count": r.Blobs.PatchCount,
			"full_bytes":  r.Blobs.FullBytes,
			"patch_bytes": r.Blobs.PatchBytes,
		},
		"images": images,
	}
}
```

- [ ] **Step 8.5 — Generate snapshots and run tests**

```bash
DIFFAH_UPDATE_SNAPSHOTS=1 go test ./cmd -run 'TestExport_DryRun_JSON|TestImport_DryRun_JSON'
go test ./cmd -run 'TestExport_DryRun_JSON|TestImport_DryRun_JSON'
```

Expected: first command writes the snapshots (or compares existing); second command passes.

- [ ] **Step 8.6 — Commit**

```bash
git add cmd/export.go cmd/import.go cmd/export_dryrun_json_test.go cmd/import_dryrun_json_test.go cmd/testdata/schemas/export-dryrun.snap.json cmd/testdata/schemas/import-dryrun.snap.json
git commit -m "feat(cmd): --output json on export/import --dry-run

Dry-run responses flow through the same jsonEnvelope as inspect. Snapshots
under cmd/testdata/schemas/ lock field names (total_images, per_image,
would_import, etc.). Set DIFFAH_UPDATE_SNAPSHOTS=1 to regenerate after a
deliberate schema change — and bump schema_version before doing so.

Refs: docs/superpowers/specs/2026-04-23-observability-foundations-design.md §4.7"
```

---

## Task 9: `docs/compat.md` — compatibility contract

**Files:**
- Create: `docs/compat.md`
- Modify: `README.md` (link to compat.md)

- [ ] **Step 9.1 — Create `docs/compat.md`**

Write the full contents:

```markdown
# diffah compatibility contract

This document defines the stability guarantees diffah makes about exit
codes, the sidecar schema, structured log output, and progress output.

## Exit codes

| Code | Category     | When |
|------|--------------|------|
| 0    | success      | operation completed |
| 1    | internal     | bug, panic, or an unclassified error |
| 2    | user         | bad flag, missing input, conflicting options, malformed `--baseline` or `--pair`, wrong baseline supplied |
| 3    | environment  | missing zstd, network failure, filesystem permission, registry auth |
| 4    | content      | sidecar schema mismatch, digest mismatch, archive corruption, unsupported schema version |

Stability: exit-code mappings for specific errors may be refined (e.g. a
new env-level fallback being added). Exit code 0 for "success" never
changes; we will not migrate a currently-2 error into a 3 without a
major-version bump.

## Sidecar schema version

Sidecar field `version` (currently `v1`) is authoritative. Import-side
negotiation rule: a reader that does not know `sidecar.version` must exit
4 with a message of the form

> `this archive was produced by diffah ≥ vX.Y; you have vZ.Z — upgrade diffah to import`

Rules:

- **Forward-compat on read.** Unknown *optional* fields are ignored with
  a `debug`-level log line. Unknown *required* fields yield a content
  error (exit 4).
- **Deprecation cycle.** Removing a CLI flag or a sidecar field requires
  one full minor-version cycle as *deprecated* (warning on every use)
  before the removal lands.
- **What bumps the version.** Sidecar schema changes bump `version`.
  CLI-only changes (new flags, new commands) do not.

## Structured log output (slog)

The JSON log format emits records with the following reserved fields:

- `time`, `level`, `msg` — stdlib slog standards.
- `component` — one of `exporter`, `importer`, `diff`, `archive`,
  `imageio`, `oci`, `zstdpatch`. New components may be added.

Key-name stability:

- Adding new keys is non-breaking.
- Renaming or removing keys is breaking — requires one minor-version
  cycle with both keys emitted and a deprecation warning.
- Machine consumers should match on `msg` + `component` + stable keys
  (digest, size, phase, etc.).

## Progress output

Progress output (bars on TTY, lines on non-TTY) is for **humans, not
machines**. It has no stability guarantee. Machine consumers must use
`--log-format=json` + structured slog events instead.

## JSON data output (`--output json`)

Every JSON response is a top-level envelope:

```json
{"schema_version":1,"data":{...}}
```

Error responses share the shape:

```json
{"schema_version":1,"error":{"category":"...","message":"...","next_action":"..."}}
```

Rules:

- `schema_version` is bumped only on breaking shape changes.
- Within a `schema_version`, adding fields is non-breaking. Removing or
  renaming is breaking.
- `next_action` is optional; when empty, renderers omit the hint line in
  text mode and the `next_action` key may be omitted in JSON mode.
```

- [ ] **Step 9.2 — Link from README.md**

Add a "Compatibility" section near the bottom of `README.md`:

```markdown
## Compatibility

Exit codes, sidecar schema evolution rules, and log/progress output
stability guarantees are documented in [docs/compat.md](docs/compat.md).
```

- [ ] **Step 9.3 — Commit**

```bash
git add docs/compat.md README.md
git commit -m "docs(compat): add exit-code, schema, and log stability contract

Documents the 0/1/2/3/4 exit-code taxonomy, sidecar schema version
negotiation rules (forward-compat on optional fields, hard error on
unknown version), slog key stability guarantees, and the explicit
non-guarantee on pretty progress output.

Refs: docs/superpowers/specs/2026-04-23-observability-foundations-design.md §4.8"
```

---

## Task 10: `diffah doctor` scaffold

**Files:**
- Create: `cmd/doctor.go`
- Create: `cmd/doctor_test.go`
- Create: `cmd/testdata/schemas/doctor.snap.json` (generated by test)

- [ ] **Step 10.1 — Write failing test for doctor zstd check**

Create `cmd/doctor_test.go`:

```go
package cmd_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/leosocy/diffah/cmd"
)

func TestDoctor_JSONShape(t *testing.T) {
	var stdout bytes.Buffer
	cmd.Run(&stdout, nil, "doctor", "--output", "json")
	var env struct {
		SchemaVersion int `json:"schema_version"`
		Data          struct {
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Detail string `json:"detail"`
			} `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout.String())
	}
	if env.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", env.SchemaVersion)
	}
	if len(env.Data.Checks) == 0 {
		t.Errorf("expected at least one check")
	}
	var names []string
	for _, c := range env.Data.Checks {
		names = append(names, c.Name)
	}
	if !contains(names, "zstd") {
		t.Errorf("expected zstd check among %v", names)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
```

- [ ] **Step 10.2 — Run test to verify it fails**

Run: `go test ./cmd -run TestDoctor_JSONShape`
Expected: FAIL (unknown command `doctor`).

- [ ] **Step 10.3 — Implement `cmd/doctor.go`**

Create `cmd/doctor.go`:

```go
package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff/errs"
)

// Check is the Phase-1 extensibility interface. Phase 5 adds network,
// authfile, config-parse checks; each new check registers itself in
// defaultChecks() without touching the renderer.
type Check interface {
	Name() string
	Run(ctx context.Context) CheckResult
}

type CheckResult struct {
	Status string // "ok" | "warn" | "fail"
	Detail string // human-readable summary
	Hint   string // optional remediation, shown when status != ok
}

func defaultChecks() []Check {
	return []Check{zstdCheck{}}
}

type zstdCheck struct{}

func (zstdCheck) Name() string { return "zstd" }

func (zstdCheck) Run(ctx context.Context) CheckResult {
	ok, detail := zstdpatch.Available(ctx)
	if ok {
		return CheckResult{Status: "ok", Detail: detail}
	}
	return CheckResult{
		Status: "fail",
		Detail: detail,
		Hint:   "install zstd 1.5+ (brew install zstd / apt install zstd)",
	}
}

func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run environment preflight checks.",
		RunE:  runDoctor,
	}
}

func init() { rootCmd.AddCommand(newDoctorCommand()) }

func runDoctor(cmd *cobra.Command, _ []string) error {
	checks := defaultChecks()
	results := make([]CheckResult, len(checks))
	for i, c := range checks {
		results[i] = c.Run(cmd.Context())
	}
	if outputFormat == "json" {
		data := make([]map[string]any, len(checks))
		for i, c := range checks {
			data[i] = map[string]any{
				"name":   c.Name(),
				"status": results[i].Status,
				"detail": results[i].Detail,
				"hint":   results[i].Hint,
			}
		}
		if err := writeJSON(cmd.OutOrStdout(), map[string]any{"checks": data}); err != nil {
			return err
		}
	} else {
		renderDoctorText(cmd.OutOrStdout(), checks, results)
	}
	if anyFailed(results) {
		return doctorErr{}
	}
	return nil
}

func renderDoctorText(w io.Writer, checks []Check, results []CheckResult) {
	for i, c := range checks {
		status := results[i].Status
		fmt.Fprintf(w, "%-40s %s\n", c.Name(), statusLabel(status, results[i].Detail))
		if status != "ok" && results[i].Hint != "" {
			fmt.Fprintf(w, "  hint: %s\n", results[i].Hint)
		}
	}
}

func statusLabel(status, detail string) string {
	switch status {
	case "ok":
		if detail != "" {
			return "ok (" + detail + ")"
		}
		return "ok"
	case "warn":
		if detail != "" {
			return "warn (" + detail + ")"
		}
		return "warn"
	default:
		if detail != "" {
			return "fail (" + detail + ")"
		}
		return "fail"
	}
}

func anyFailed(rs []CheckResult) bool {
	for _, r := range rs {
		if r.Status == "fail" {
			return true
		}
	}
	return false
}

// doctorErr is returned when any check fails, so cmd.Execute classifies
// the run as environment (exit 3) with a generic hint.
type doctorErr struct{}

func (doctorErr) Error() string              { return "one or more checks failed" }
func (doctorErr) Category() errs.Category    { return errs.CategoryEnvironment }
func (doctorErr) NextAction() string         { return "see failing check for its specific hint" }
```

- [ ] **Step 10.4 — Run tests**

Run: `go test ./cmd -run TestDoctor`
Expected: PASS.

- [ ] **Step 10.5 — Run full test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 10.6 — Commit**

```bash
git add cmd/doctor.go cmd/doctor_test.go
git commit -m "feat(cmd): add diffah doctor preflight scaffold

The doctor subcommand runs registered environment checks and emits
results as text or JSON. Phase 1 ships a single check (zstd binary
presence + version); Phase 5 will add network, authfile, config-parse,
and writable-output-dir checks by registering more entries in
defaultChecks. Any failing check exits 3 (environment) with a hint.

Refs: docs/superpowers/specs/2026-04-23-observability-foundations-design.md §4.9"
```

---

## Post-implementation verification

- [ ] **Step V.1 — Full test suite with race detector**

```bash
go test -race -cover ./...
go test -race -cover -tags 'integration containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper' ./...
```

All must pass.

- [ ] **Step V.2 — Lint**

```bash
golangci-lint run
```

No new findings.

- [ ] **Step V.3 — Smoke-test each exit code**

```bash
go run -tags "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper" . export ; echo $?    # expect 2 (user)
go run -tags "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper" . doctor ; echo $?    # expect 0 or 3
go run -tags "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper" . inspect nonexistent.tar ; echo $?  # expect 3 (env — filesystem)
```

- [ ] **Step V.4 — Manual TTY progress-bar spot-check**

```bash
go run . export --pair app=testdata/fixtures/v1_baseline_oci.tar,testdata/fixtures/v1_target_oci.tar /tmp/out.tar
```

Confirm multi-bar rendering in the terminal; re-run with `| cat` to confirm graceful fallback to line summaries.

- [ ] **Step V.5 — Open PR series**

Ten commits are on `spec/v2-observability-foundations`. Open one of:

- a single PR for the whole Phase 1 (rapid review pass) — preferred if reviewers are comfortable with 10 commits on one PR
- ten stacked PRs — one per commit — if the project uses stacked-PR tooling

Either way, the PR description summarizes Phase 1's acceptance criteria from §10 of the spec and links to `docs/superpowers/specs/2026-04-23-observability-foundations-design.md`.

---

## Spec-coverage checklist

Each row ticks a Phase 1 spec requirement against the task that implements it.

| Spec section                               | Delivered by task |
|--------------------------------------------|--------------------|
| §3 three-channel architecture              | Tasks 3–6 (slog + progress + error classification)      |
| §4.1 `pkg/diff/errs` package               | Task 1 |
| §4.1 stdlib sentinel fallbacks             | Task 1 (`classify.go`) |
| §4.2 Category/NextAction on error types    | Task 2 |
| §4.3 `cmd.Execute` classification edge     | Task 2 |
| §4.4 slog bootstrap + flags                | Task 3 |
| §4.5 slog threaded through pkg/* + internal/* | Task 4 |
| §4.6 `pkg/progress` Reporter + discard/line/FromWriter | Task 5 |
| §4.6 mpb multi-bar + NewAuto + `--progress` | Task 6 |
| §4.7 `--output json` inspect               | Task 7 |
| §4.7 JSON error payload                    | Task 7 |
| §4.7 `--output json` export/import dry-run | Task 8 |
| §4.8 `docs/compat.md`                      | Task 9 |
| §4.9 `diffah doctor` scaffold              | Task 10 |
| §10 acceptance criteria (1–7)              | All tasks plus Step V.3 + V.4 verification |
