# Phase 5.1 — Doctor Expansion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expand `diffah doctor` from one check (zstd) to five checks (zstd, tmpdir, authfile, network, config). The new `network` check is gated by a doctor-local `--probe` flag and exercises the registry over a 15-second hard-timeout. The `config` check exercises the YAML loader shipped in PR-1. Doctor is exempted from the persistent config-load hook so it can diagnose a malformed `~/.diffah/config.yaml`.

**Architecture:** Four new `Check` implementations live in a new file `cmd/doctor_checks.go`. The existing `Check` interface is unchanged. `defaultChecks` gains two parameters (`probe string`, `buildSysCtx registryContextBuilder`) so the slice composition stays the single source of truth. `newDoctorCommand` calls `installRegistryFlags(c)` to register the same 10 registry flags as `diff` / `bundle` / `apply` / `unbundle`, plus a doctor-local `--probe`. The root `PersistentPreRunE` predicate `isConfigSubtree` is renamed `isExemptFromConfigLoad` and grows a `|| cmd.Name() == "doctor"` branch. The package-private `internal/imageio.defaultAuthFile()` is exported as `imageio.ResolveAuthFile()` so doctor's authfile check reuses the same lookup chain.

**Tech Stack:** Go 1.25, `github.com/spf13/cobra` (existing), `github.com/stretchr/testify/require`, `pkg/diff/errs` taxonomy, `pkg/diff.ClassifyRegistryErr`, `pkg/config` (Load / Validate / DefaultPath — shipped in PR-1 at f275485), `internal/imageio` (ParseReference / BuildSystemContext / SystemContextFlags / ResolveAuthFile after Task 2), `internal/registrytest` for integration tests, `go.podman.io/image/v5` transitively.

**Spec reference:** `docs/superpowers/specs/2026-04-29-phase5-dx-polish-design.md` §6.

**Brainstorm decisions on top of §6:**

- **D1.** Rename `cmd/root.go`'s `isConfigSubtree(cmd)` predicate to `isExemptFromConfigLoad(cmd)`; add `|| cmd.Name() == "doctor"`.
- **D2.** Export `internal/imageio.defaultAuthFile()` → `ResolveAuthFile()`. Update the same-package caller (`applyAuthFile`).
- **D3.** `networkCheck` wraps the probe in `context.WithTimeout(parentCtx, 15*time.Second)`; errors are classified via `diff.ClassifyRegistryErr`. Integration test must include a black-hole listener case to lock the timeout invariant.
- **D4.** Doctor uses the full `installRegistryFlags(cmd)` (10 flags, identical to other commands). `--probe` is doctor-local. `defaultChecks(probe string, buildSysCtx registryContextBuilder) []Check`; `networkCheck` stores both and lazily calls `buildSysCtx()` inside `Run` only when `probe` is non-empty.

**Out of scope** (per spec §3 / §11):

- No registry-cred liveness check beyond `--probe` (no automatic ping of every cred in authfile).
- No new doctor flags beyond `--probe` and the 10 from `installRegistryFlags`.
- No timeout flag (`--probe-timeout`); 15 s is hardcoded.

---

## File plan

| File | Action | Responsibility |
|---|---|---|
| `internal/imageio/sysctx.go` | modify | Rename `defaultAuthFile` → `ResolveAuthFile` (export), update internal caller |
| `cmd/root.go` | modify | Rename `isConfigSubtree` → `isExemptFromConfigLoad`; add doctor branch |
| `cmd/doctor.go` | modify | `defaultChecks(probe, buildSysCtx)`; install registry flags + `--probe` in `newDoctorCommand` |
| `cmd/doctor_checks.go` | create | `tmpdirCheck`, `authfileCheck`, `networkCheck`, `configCheck` structs |
| `cmd/doctor_internal_test.go` | modify | Internal-package unit tests for new check helpers (authfile JSON parse, tmpdir probe write, network skip+ref-error, config) |
| `cmd/doctor_test.go` | modify | External-package JSON shape + table tests for the five-check slice |
| `cmd/doctor_integration_test.go` | create | Build tag `integration`; tests `--probe` against `internal/registrytest` (success, 401, manifest-missing, black-hole 15 s timeout) |
| `CHANGELOG.md` | modify | Phase 5.1 entry under `[Unreleased]` |

---

## Phase 1 — Branch + foundation refactors

### Task 1: Create branch from master

**Files:**
- (none — git only)

- [ ] **Step 1: Confirm clean tree on master**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git status --short
git rev-parse --abbrev-ref HEAD
git log --oneline -1
```

Expected: working tree clean, on `master`, latest commit is `f275485 feat(config): YAML config file (Phase 5.2) (#32)`. If the tree is dirty, stash or commit first.

- [ ] **Step 2: Create branch**

```bash
git switch -c spec/phase5-doctor
```

(If the branch already exists from a prior session, `git switch spec/phase5-doctor` and continue from the next pending step.)

- [ ] **Step 3: Verify CI baseline**

```bash
go build ./...
go test ./cmd/ ./pkg/config/ ./internal/imageio/ -run . -count=1 -short
```

Expected: all PASS. Establishes that whatever follows is caused by *our* edits, not pre-existing breakage.

### Task 2: Export `imageio.ResolveAuthFile`

**Files:**
- Modify: `internal/imageio/sysctx.go`

- [ ] **Step 1: Check for existing `defaultAuthFile` callers and tests**

```bash
grep -rn "defaultAuthFile" --include="*.go" .
```

Expected: only one call site (`internal/imageio/sysctx.go:71` inside `applyAuthFile`) plus the definition itself at `:127`. No existing test directly references `defaultAuthFile` (it's exercised indirectly via `BuildSystemContext`). If grep finds anything else, include it in this task's edit set.

- [ ] **Step 2: Edit `internal/imageio/sysctx.go`**

Replace the `defaultAuthFile` definition (lines 123–146) with:

```go
// ResolveAuthFile returns the first existing file in the standard
// containers-image precedence chain:
//
//  1. $REGISTRY_AUTH_FILE
//  2. $XDG_RUNTIME_DIR/containers/auth.json
//  3. $HOME/.docker/config.json
//
// Returns an empty string when none of the candidates exist (upstream
// containers-image treats this as "no credentials available"). Callers
// outside this package use this for diagnostic display (e.g., diffah
// doctor's authfile check); callers inside the package use it to seed
// SystemContext.AuthFilePath.
func ResolveAuthFile() string {
	if v := os.Getenv("REGISTRY_AUTH_FILE"); v != "" {
		if fileExists(v) {
			return v
		}
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		candidate := filepath.Join(xdg, "containers", "auth.json")
		if fileExists(candidate) {
			return candidate
		}
	}
	if home := os.Getenv("HOME"); home != "" {
		candidate := filepath.Join(home, ".docker", "config.json")
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}
```

Update the internal caller `applyAuthFile` (line 71) — change `sc.AuthFilePath = defaultAuthFile()` to `sc.AuthFilePath = ResolveAuthFile()`.

- [ ] **Step 3: Build + test**

```bash
go build ./...
go test ./internal/imageio/ -count=1
```

Expected: PASS — the rename preserves identical runtime behavior, just exposes the function.

- [ ] **Step 4: Commit**

```bash
git add internal/imageio/sysctx.go
git commit -m "refactor(imageio): export ResolveAuthFile

Renamed defaultAuthFile -> ResolveAuthFile so diffah doctor's
authfile check can reuse the same containers-image precedence
chain (\$REGISTRY_AUTH_FILE > \$XDG_RUNTIME_DIR > \$HOME). The
internal applyAuthFile caller is updated. No runtime behavior
change.

Refs: docs/superpowers/plans/2026-04-29-phase5-doctor.md Task 2"
```

### Task 3: Rename `isConfigSubtree` → `isExemptFromConfigLoad`

**Files:**
- Modify: `cmd/root.go`

- [ ] **Step 1: Edit `cmd/root.go`**

Replace the predicate function and its caller. Find the existing block at lines 161–172:

```go
// isConfigSubtree reports whether cmd is the 'config' command or one
// of its subcommands. Used by PersistentPreRunE to skip the config
// load+apply step so 'config validate' / 'config show' / etc. work
// even when the resolved config file is malformed.
func isConfigSubtree(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "config" {
			return true
		}
	}
	return false
}
```

Replace with:

```go
// isExemptFromConfigLoad reports whether cmd should skip the persistent
// config load+apply step. Two reasons to exempt:
//
//  1. The 'config' subtree (show/init/validate) MUST run even when the
//     resolved config file is malformed — that's how operators diagnose
//     the breakage.
//
//  2. The 'doctor' command is the diagnostic escape hatch and must be
//     able to report a malformed config structurally rather than
//     hard-failing in PersistentPreRunE before runDoctor fires.
func isExemptFromConfigLoad(cmd *cobra.Command) bool {
	if cmd.Name() == "doctor" {
		return true
	}
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "config" {
			return true
		}
	}
	return false
}
```

Update the only call site (line 140):

```go
		if !isExemptFromConfigLoad(cmd) {
			if err := loadAndApplyConfig(cmd); err != nil {
				return err
			}
		}
```

- [ ] **Step 2: Update the inline comment above the call site** (line 136–139). Change "The 'config' subtree must run even when…" to:

```go
		// The 'config' subtree and 'doctor' must run even when the resolved
		// config file is malformed — that's how operators diagnose the
		// breakage. Skip the persistent load+apply for those commands.
```

- [ ] **Step 3: Build + run cmd tests**

```bash
go build ./...
go test ./cmd/ -count=1 -short
```

Expected: PASS. The renaming preserves behavior for the existing `config` subtree; the new `doctor` branch is dormant until the doctor is actually broken.

- [ ] **Step 4: Commit**

```bash
git add cmd/root.go
git commit -m "refactor(cmd): rename isConfigSubtree → isExemptFromConfigLoad

doctor must run even when ~/.diffah/config.yaml is malformed,
otherwise the diagnostic tool can't diagnose its own config
failure (PersistentPreRunE would hard-fail at exit 2 before
runDoctor fires). Generalize the 'config' subtree exemption
into isExemptFromConfigLoad with a doctor-name branch.

Refs: docs/superpowers/plans/2026-04-29-phase5-doctor.md Task 3"
```

### Task 4: Refactor `defaultChecks` signature; install registry flags + `--probe`

**Files:**
- Modify: `cmd/doctor.go`
- Modify: `cmd/doctor_internal_test.go` (test signature update)

- [ ] **Step 1: Read existing `cmd/doctor.go` carefully** (already done during planning — review lines 31–60 again before editing).

- [ ] **Step 2: Edit `cmd/doctor.go`** — replace the entire file contents with:

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

const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

type Check interface {
	Name() string
	Run(ctx context.Context) CheckResult
}

type CheckResult struct {
	Status string
	Detail string
	Hint   string
}

// defaultChecks returns the five checks that 'diffah doctor' runs in
// order: zstd (binary version), tmpdir (write probe), authfile (lookup
// chain + JSON parse), network (manifest GetManifest under 15s timeout,
// gated by --probe), and config (pkg/config.Validate against
// DefaultPath). probe is the value of the --probe flag (empty = skip
// network probe). buildSysCtx materializes a *types.SystemContext from
// the registry-flag block installed on the doctor command.
func defaultChecks(probe string, buildSysCtx registryContextBuilder) []Check {
	return []Check{
		zstdCheck{},
		tmpdirCheck{},
		authfileCheck{},
		networkCheck{probe: probe, buildSysCtx: buildSysCtx},
		configCheck{},
	}
}

type zstdCheck struct{}

func (zstdCheck) Name() string { return "zstd" }

func (zstdCheck) Run(ctx context.Context) CheckResult {
	ok, detail := zstdpatch.AvailableDetail(ctx)
	if ok {
		return CheckResult{Status: statusOK, Detail: detail}
	}
	return CheckResult{
		Status: statusFail,
		Detail: detail,
		Hint:   "install zstd 1.5+ (brew install zstd / apt install zstd)",
	}
}

func newDoctorCommand() *cobra.Command {
	var probe string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run environment preflight checks.",
		Long: `Run environment preflight checks. Five checks are executed in order:

  zstd      — zstd binary on $PATH and version >= 1.5
  tmpdir    — $TMPDIR (or os.TempDir()) accepts a 1 KiB write
  authfile  — $REGISTRY_AUTH_FILE / $XDG_RUNTIME_DIR / $HOME chain
              resolves to a parseable JSON file with an 'auths' map
  network   — (skipped unless --probe is given) the supplied registry
              reference responds to GetManifest within 15 s
  config    — ~/.diffah/config.yaml (or $DIFFAH_CONFIG) is absent or
              parses cleanly

Exits 3 if any check fails (CategoryEnvironment); warnings do not
change the exit code.`,
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&probe, "probe", "",
		"image reference (e.g., docker://example.com/foo:tag) for the network check")
	buildSysCtx := installRegistryFlags(cmd)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		return runDoctor(c, probe, buildSysCtx)
	}
	return cmd
}

func init() { rootCmd.AddCommand(newDoctorCommand()) }

func runDoctor(cmd *cobra.Command, probe string, buildSysCtx registryContextBuilder) error {
	checks := defaultChecks(probe, buildSysCtx)
	results := make([]CheckResult, len(checks))
	for i, c := range checks {
		results[i] = c.Run(cmd.Context())
	}

	if outputFormat == outputJSON {
		data := make([]map[string]any, len(checks))
		for i, c := range checks {
			entry := map[string]any{
				"name":   c.Name(),
				"status": results[i].Status,
				"detail": results[i].Detail,
			}
			if results[i].Hint != "" {
				entry["hint"] = results[i].Hint
			}
			data[i] = entry
		}
		if err := writeJSON(cmd.OutOrStdout(), map[string]any{"checks": data}); err != nil {
			return err
		}
	} else {
		renderDoctorText(cmd.OutOrStdout(), checks, results)
	}

	if anyFailed(results) {
		return errDoctorChecksFailed
	}
	return nil
}

func renderDoctorText(w io.Writer, checks []Check, results []CheckResult) {
	for i, c := range checks {
		fmt.Fprintf(w, "%-40s %s\n", c.Name(), statusLabel(results[i].Status, results[i].Detail))
		if results[i].Status != statusOK && results[i].Hint != "" {
			fmt.Fprintf(w, "  hint: %s\n", results[i].Hint)
		}
	}
}

func statusLabel(status, detail string) string {
	switch status {
	case statusOK:
		if detail != "" {
			return "ok (" + detail + ")"
		}
		return "ok"
	case statusWarn:
		if detail != "" {
			return statusWarn + " (" + detail + ")"
		}
		return statusWarn
	case statusFail:
		if detail != "" {
			return "fail (" + detail + ")"
		}
		return "fail"
	default:
		if detail != "" {
			return status + " (" + detail + ")"
		}
		return status
	}
}

func anyFailed(rs []CheckResult) bool {
	for _, r := range rs {
		if r.Status == statusFail {
			return true
		}
	}
	return false
}

type doctorChecksFailed struct{}

func (doctorChecksFailed) Error() string           { return "one or more checks failed" }
func (doctorChecksFailed) Category() errs.Category { return errs.CategoryEnvironment }
func (doctorChecksFailed) NextAction() string      { return "see failing check for its specific hint" }

var errDoctorChecksFailed error = doctorChecksFailed{}
```

Note: this introduces references to `tmpdirCheck`, `authfileCheck`, `networkCheck`, `configCheck` that don't exist yet — Tasks 5–8 add them. Build will fail until Task 8 lands. **Do not commit yet** — Phase 2 commits everything at the end of Task 8.

- [ ] **Step 3: Update internal test that calls `defaultChecks()` directly**

`cmd/doctor_internal_test.go` lines 65–72 currently have:

```go
func TestDefaultChecks_ContainsZstd(t *testing.T) {
	checks := defaultChecks()
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name()
	}
	require.Contains(t, names, "zstd")
}
```

Replace with:

```go
func TestDefaultChecks_ReturnsFiveChecksInOrder(t *testing.T) {
	checks := defaultChecks("", nil)
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name()
	}
	require.Equal(t, []string{"zstd", "tmpdir", "authfile", "network", "config"}, names)
}
```

- [ ] **Step 4: Build (will fail — that's expected; Tasks 5–8 add the missing types)**

```bash
go build ./cmd/ 2>&1 | head -20
```

Expected: `undefined: tmpdirCheck`, `undefined: authfileCheck`, `undefined: networkCheck`, `undefined: configCheck`. **Do not commit yet.** Move on to Task 5.

---

## Phase 2 — New checks (TDD)

Phase 2 accumulates `cmd/doctor_checks.go` over four tasks. Each task adds one struct + its tests; only Task 8 runs the full quality gate and commits the whole phase as one logical unit.

### Task 5: `tmpdirCheck`

**Files:**
- Create: `cmd/doctor_checks.go`
- Modify: `cmd/doctor_internal_test.go`

- [ ] **Step 1: Append failing tests to `cmd/doctor_internal_test.go`**

Append after the existing tests:

```go
func TestTmpdirCheck_NameAndOK(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	c := tmpdirCheck{}
	require.Equal(t, "tmpdir", c.Name())
	result := c.Run(context.Background())
	require.Equal(t, statusOK, result.Status)
	require.NotEmpty(t, result.Detail)
	require.Empty(t, result.Hint)
}

func TestTmpdirCheck_FailWhenDirDoesNotExist(t *testing.T) {
	t.Setenv("TMPDIR", "/nonexistent/diffah-doctor-test/path")
	result := tmpdirCheck{}.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.NotEmpty(t, result.Detail)
	require.NotEmpty(t, result.Hint)
}
```

- [ ] **Step 2: Run tests, verify failure**

```bash
go test ./cmd/ -run TestTmpdirCheck -v -count=1
```

Expected: build error `undefined: tmpdirCheck`.

- [ ] **Step 3: Create `cmd/doctor_checks.go`**

```go
package cmd

import (
	"context"
	"fmt"
	"os"
)

type tmpdirCheck struct{}

func (tmpdirCheck) Name() string { return "tmpdir" }

// Run writes a 1 KiB probe file into os.TempDir() (which honours
// $TMPDIR) and removes it. Any error along the way -> Fail.
func (tmpdirCheck) Run(_ context.Context) CheckResult {
	dir := os.TempDir()
	f, err := os.CreateTemp(dir, "diffah-doctor-*.probe")
	if err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("create probe in %s: %v", dir, err),
			Hint:   "ensure $TMPDIR is writable, or set TMPDIR to a writable directory",
		}
	}
	probePath := f.Name()
	defer os.Remove(probePath)
	if _, err := f.Write(make([]byte, 1024)); err != nil {
		_ = f.Close()
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("write probe to %s: %v", probePath, err),
			Hint:   "ensure $TMPDIR has free space",
		}
	}
	if err := f.Close(); err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("close probe %s: %v", probePath, err),
			Hint:   "filesystem may be flaky; check dmesg",
		}
	}
	return CheckResult{Status: statusOK, Detail: dir}
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./cmd/ -run TestTmpdirCheck -v -count=1
```

Expected: PASS for both cases.

- [ ] **Step 5: Do NOT commit yet** — Phase 2 tasks accumulate into `cmd/doctor_checks.go`. Single commit at the end of Task 8.

### Task 6: `authfileCheck`

**Files:**
- Modify: `cmd/doctor_checks.go`
- Modify: `cmd/doctor_internal_test.go`

- [ ] **Step 1: Append failing tests to `cmd/doctor_internal_test.go`**

```go
func TestAuthfileCheck_WarnWhenChainEmpty(t *testing.T) {
	// Empty all three env vars so ResolveAuthFile returns "".
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir()) // empty home dir — no .docker/config.json

	result := authfileCheck{}.Run(context.Background())
	require.Equal(t, statusWarn, result.Status)
	require.Contains(t, result.Detail, "anonymous pulls only")
}

func TestAuthfileCheck_OKForValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"auths": {
			"registry.example.com": {"auth": "abc"},
			"docker.io": {"auth": "def"}
		}
	}`), 0o600))
	t.Setenv("REGISTRY_AUTH_FILE", path)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir())

	result := authfileCheck{}.Run(context.Background())
	require.Equal(t, statusOK, result.Status)
	require.Contains(t, result.Detail, path)
	require.Contains(t, result.Detail, "2 registries")
}

func TestAuthfileCheck_FailOnMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(path, []byte("{this is not json"), 0o600))
	t.Setenv("REGISTRY_AUTH_FILE", path)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir())

	result := authfileCheck{}.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.Contains(t, result.Detail, "JSON parse error")
	require.NotEmpty(t, result.Hint)
}

func TestAuthfileCheck_FailWhenAuthsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"other": {}}`), 0o600))
	t.Setenv("REGISTRY_AUTH_FILE", path)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir())

	result := authfileCheck{}.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.Contains(t, result.Detail, "missing 'auths' map")
}
```

Add to the test file's import block (if not already present): `"os"`, `"path/filepath"`.

- [ ] **Step 2: Run tests, verify failure**

```bash
go test ./cmd/ -run TestAuthfileCheck -v -count=1
```

Expected: build error `undefined: authfileCheck`.

- [ ] **Step 3: Append to `cmd/doctor_checks.go`**

Update the import block to:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/leosocy/diffah/internal/imageio"
)
```

Append below `tmpdirCheck`:

```go
type authfileCheck struct{}

func (authfileCheck) Name() string { return "authfile" }

// Run resolves the standard containers-image authfile lookup chain via
// imageio.ResolveAuthFile, then reads the file and verifies it parses
// as JSON containing an 'auths' map. No registry round-trip is made
// here — that's --probe's job.
func (authfileCheck) Run(_ context.Context) CheckResult {
	path := imageio.ResolveAuthFile()
	if path == "" {
		return CheckResult{
			Status: statusWarn,
			Detail: "no authfile found in lookup chain; anonymous pulls only",
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("resolved: %s — read error: %v", path, err),
			Hint:   "ensure the file is readable by the current user",
		}
	}
	var parsed struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("resolved: %s — JSON parse error: %v", path, err),
			Hint:   "fix the JSON file or unset $REGISTRY_AUTH_FILE",
		}
	}
	if parsed.Auths == nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("resolved: %s — missing 'auths' map", path),
			Hint:   "regenerate via 'docker login' or 'podman login'",
		}
	}
	return CheckResult{
		Status: statusOK,
		Detail: fmt.Sprintf("resolved: %s (%d registries configured)", path, len(parsed.Auths)),
	}
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./cmd/ -run TestAuthfileCheck -v -count=1
```

Expected: PASS for all four cases.

- [ ] **Step 5: Do NOT commit yet** — see Task 8.

### Task 7: `networkCheck`

**Files:**
- Modify: `cmd/doctor_checks.go`
- Modify: `cmd/doctor_internal_test.go`

- [ ] **Step 1: Append failing tests to `cmd/doctor_internal_test.go`**

```go
func TestNetworkCheck_SkippedWhenProbeEmpty(t *testing.T) {
	c := networkCheck{probe: "", buildSysCtx: nil}
	result := c.Run(context.Background())
	require.Equal(t, statusOK, result.Status)
	require.Contains(t, result.Detail, "skipped")
}

func TestNetworkCheck_FailWhenBuildSysCtxFails(t *testing.T) {
	stub := func() (*types.SystemContext, int, time.Duration, error) {
		return nil, 0, 0, errors.New("flag conflict: --creds and --no-creds")
	}
	c := networkCheck{probe: "docker://example.com/foo:tag", buildSysCtx: stub}
	result := c.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.Contains(t, result.Detail, "flag conflict")
	require.NotEmpty(t, result.Hint)
}

func TestNetworkCheck_FailOnInvalidReference(t *testing.T) {
	stub := func() (*types.SystemContext, int, time.Duration, error) {
		return &types.SystemContext{}, 0, 0, nil
	}
	c := networkCheck{probe: "not a valid reference", buildSysCtx: stub}
	result := c.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.Contains(t, result.Detail, "parse")
}
```

Add to test imports: `"errors"`, `"time"`, `"go.podman.io/image/v5/types"`.

- [ ] **Step 2: Run tests, verify failure**

```bash
go test ./cmd/ -run TestNetworkCheck -v -count=1
```

Expected: build error `undefined: networkCheck`.

- [ ] **Step 3: Append to `cmd/doctor_checks.go`**

Update the import block to:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
)
```

Add this constant near the top (just after the imports, before `tmpdirCheck`):

```go
// probeTimeout bounds the network check so a black-holed registry
// never hangs the diagnostic tool. Hardcoded — no flag yet.
const probeTimeout = 15 * time.Second
```

Append below `authfileCheck`:

```go
// networkCheck round-trips a single GetManifest against the registry
// reference supplied via --probe, with a 15s hard cap. Errors are
// classified through diff.ClassifyRegistryErr so the Detail string
// uses the same vocabulary as the importer's error messages.
type networkCheck struct {
	probe       string
	buildSysCtx registryContextBuilder
}

func (networkCheck) Name() string { return "network" }

func (n networkCheck) Run(parentCtx context.Context) CheckResult {
	if n.probe == "" {
		return CheckResult{
			Status: statusOK,
			Detail: "--probe not supplied; check skipped",
		}
	}
	if n.buildSysCtx == nil {
		return CheckResult{
			Status: statusFail,
			Detail: "internal: registry context builder is nil",
			Hint:   "this is a bug; please file an issue",
		}
	}
	sysctx, _, _, err := n.buildSysCtx()
	if err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: err.Error(),
			Hint:   "verify --authfile / --tls-verify / --cert-dir / --creds flag combination",
		}
	}
	ref, err := imageio.ParseReference(n.probe)
	if err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: err.Error(),
			Hint:   "use 'docker://registry/name:tag' (or another supported transport)",
		}
	}
	ctx, cancel := context.WithTimeout(parentCtx, probeTimeout)
	defer cancel()
	src, err := ref.NewImageSource(ctx, sysctx)
	if err != nil {
		return classifyAndFailNetwork(err, n.probe)
	}
	defer src.Close()
	if _, _, err := src.GetManifest(ctx, nil); err != nil {
		return classifyAndFailNetwork(err, n.probe)
	}
	return CheckResult{
		Status: statusOK,
		Detail: fmt.Sprintf("manifest reachable: %s", n.probe),
	}
}

func classifyAndFailNetwork(err error, ref string) CheckResult {
	classified := diff.ClassifyRegistryErr(err, ref)
	return CheckResult{
		Status: statusFail,
		Detail: classified.Error(),
		Hint:   "check connectivity, credentials, or TLS configuration",
	}
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./cmd/ -run TestNetworkCheck -v -count=1
```

Expected: PASS for all three cases. The 4th case (real probe) is integration-only — Phase 3.

- [ ] **Step 5: Do NOT commit yet** — see Task 8.

### Task 8: `configCheck` + commit Phase 2

**Files:**
- Modify: `cmd/doctor_checks.go`
- Modify: `cmd/doctor_internal_test.go`
- Modify: `cmd/doctor_test.go` (external-package shape test — strengthen)

- [ ] **Step 1: Append failing tests to `cmd/doctor_internal_test.go`**

```go
func TestConfigCheck_OKWhenFileAbsent(t *testing.T) {
	t.Setenv("DIFFAH_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	result := configCheck{}.Run(context.Background())
	require.Equal(t, statusOK, result.Status)
	require.Contains(t, result.Detail, "no config file")
}

func TestConfigCheck_OKForValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("platform: linux/arm64\n"), 0o600))
	t.Setenv("DIFFAH_CONFIG", path)

	result := configCheck{}.Run(context.Background())
	require.Equal(t, statusOK, result.Status)
	require.Contains(t, result.Detail, path)
}

func TestConfigCheck_FailForMalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("not: valid: yaml: ["), 0o600))
	t.Setenv("DIFFAH_CONFIG", path)

	result := configCheck{}.Run(context.Background())
	require.Equal(t, statusFail, result.Status)
	require.Contains(t, result.Detail, "config")
	require.NotEmpty(t, result.Hint)
}
```

- [ ] **Step 2: Run tests, verify failure**

```bash
go test ./cmd/ -run TestConfigCheck -v -count=1
```

Expected: build error `undefined: configCheck`.

- [ ] **Step 3: Append to `cmd/doctor_checks.go`**

Update the import block to:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/config"
	"github.com/leosocy/diffah/pkg/diff"
)
```

Append below `networkCheck`:

```go
// configCheck calls pkg/config.Validate against the resolved
// DefaultPath. A missing file is OK (defaults are used); a present
// file that fails to parse is Fail. Doctor must be exempted from the
// persistent config-load hook in cmd/root.go for this check to fire
// when the file is malformed (see isExemptFromConfigLoad).
type configCheck struct{}

func (configCheck) Name() string { return "config" }

func (configCheck) Run(_ context.Context) CheckResult {
	path := config.DefaultPath()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return CheckResult{
			Status: statusOK,
			Detail: "no config file (defaults in use)",
		}
	} else if err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("%s: %v", path, err),
			Hint:   "ensure the config file is readable by the current user",
		}
	}
	if err := config.Validate(path); err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: err.Error(),
			Hint:   "run 'diffah config validate' for the same diagnostic; or unset $DIFFAH_CONFIG",
		}
	}
	return CheckResult{
		Status: statusOK,
		Detail: fmt.Sprintf("loaded ok: %s", path),
	}
}
```

- [ ] **Step 4: Build whole tree**

```bash
go build ./...
```

Expected: clean build (no missing symbols).

- [ ] **Step 5: Run all doctor tests**

```bash
go test ./cmd/ -run TestTmpdirCheck -v -count=1
go test ./cmd/ -run TestAuthfileCheck -v -count=1
go test ./cmd/ -run TestNetworkCheck -v -count=1
go test ./cmd/ -run TestConfigCheck -v -count=1
go test ./cmd/ -run TestDefaultChecks -v -count=1
go test ./cmd/ -run TestDoctor -v -count=1
```

Expected: all PASS. The original `TestDoctor_JSONShape` (external package) may need a tweak — see Step 6.

- [ ] **Step 6: Strengthen `cmd/doctor_test.go`'s `TestDoctor_JSONShape`**

The existing test asserts `>= 1 check named zstd`. With five checks now, lock the full set. In `cmd/doctor_test.go`, replace the name-collection block (lines ~31–37) with:

```go
	wanted := []string{"zstd", "tmpdir", "authfile", "network", "config"}
	gotNames := make(map[string]bool)
	for _, c := range env.Data.Checks {
		gotNames[c.Name] = true
	}
	for _, name := range wanted {
		require.True(t, gotNames[name], "expected check %q in %v", name, env.Data.Checks)
	}
```

If the `containsStr` helper at the bottom of the file becomes unused, delete it (the test file should still compile clean).

Also seed `DIFFAH_CONFIG` and authfile env to absent paths inside this test to avoid leaking the developer's environment into the assertion (`unit_setup_test.go`'s TestMain already does this for `DIFFAH_CONFIG`; authfile envs are not yet isolated). For now, only assert names and statuses, not detail content — that keeps the test environment-tolerant. The status assertions in the existing block (`require.Contains(t, []string{"ok","warn","fail"}, c.Status, ...)`) are already environment-tolerant; keep them.

- [ ] **Step 7: Run external-package tests**

```bash
go test ./cmd/ -run TestDoctor_ -v -count=1
```

Expected: PASS.

- [ ] **Step 8: Lint the new file**

```bash
golangci-lint run ./cmd/...
```

Expected: clean. If lint complains about anything (unused imports, error wrapping, etc.), fix it now — Phase 2's commit must lint clean.

- [ ] **Step 9: Commit Phase 2**

```bash
git add cmd/doctor.go cmd/doctor_checks.go cmd/doctor_internal_test.go cmd/doctor_test.go
git commit -m "feat(doctor): tmpdir / authfile / network / config checks

Five checks now run in 'diffah doctor': zstd (existing), tmpdir
(write probe to \$TMPDIR), authfile (containers-image lookup
chain + JSON parse via imageio.ResolveAuthFile), network
(GetManifest under 15s timeout, gated by --probe), config
(pkg/config.Validate against \$DIFFAH_CONFIG > ~/.diffah/
config.yaml).

defaultChecks now takes (probe, buildSysCtx) so the slice
composition stays the single source of truth. newDoctorCommand
calls installRegistryFlags(c) so --probe inherits the same
10-flag block as diff/bundle/apply/unbundle (--authfile,
--tls-verify, --cert-dir, etc.). The --probe flag itself is
doctor-local.

Spec: docs/superpowers/specs/2026-04-29-phase5-dx-polish-design.md §6
Refs: docs/superpowers/plans/2026-04-29-phase5-doctor.md Tasks 4–8"
```

---

## Phase 3 — Integration coverage

### Task 9: `cmd/doctor_integration_test.go`

**Files:**
- Create: `cmd/doctor_integration_test.go`

The integration test exercises `--probe` against an in-process registry from `internal/registrytest`. It must include four cases:

1. **Success.** Plain `registrytest.New(t)`, seeded with one image; probe pulls its manifest → exit 0.
2. **401.** `registrytest.New(t, registrytest.WithBasicAuth(...))`; probe with `--no-creds` → exit 3 with "unauthorized".
3. **Manifest missing.** Plain registry, no seed, probe a missing tag → exit 3 with "manifest unknown" / "name unknown" / "not found".
4. **Black-hole timeout.** A plain `net.Listener` that accepts but never responds; probe must Fail within ~15 s (D3 invariant).

- [ ] **Step 1: Locate the existing test helpers**

```bash
grep -n "func seedOCIIntoRegistry\|func registryDockerURL\|func registryHost" cmd/*.go
```

These helpers are defined in the existing integration tests (e.g., `cmd/apply_registry_integration_test.go` referenced them at lines 44, 53). Use whatever signatures are found. Do not redefine them.

- [ ] **Step 2: Create `cmd/doctor_integration_test.go`**

```go
//go:build integration

package cmd_test

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

// TestDoctorProbe_OKAgainstSeededRegistry asserts a successful manifest
// fetch (network check status=ok, exit code 0).
func TestDoctorProbe_OKAgainstSeededRegistry(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedOCIIntoRegistry(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)

	stdout, stderr, exit := runDiffahBin(t, bin,
		"doctor",
		"--probe", registryDockerURL(t, srv, "app/v1"),
		"--tls-verify=false",
	)
	require.Equalf(t, 0, exit, "doctor failed: stdout=%s stderr=%s", stdout, stderr)
	require.Containsf(t, stdout, "network", "expected network check in output: %s", stdout)
	require.NotContainsf(t, stdout, "fail (", "no check should fail: %s", stdout)
}

// TestDoctorProbe_FailOn401 asserts that an unauthenticated probe
// against a Basic-auth-guarded registry causes the network check to
// fail and the process to exit 3 (CategoryEnvironment).
func TestDoctorProbe_FailOn401(t *testing.T) {
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))

	stdout, stderr, exit := runDiffahBin(t, bin,
		"doctor",
		"--probe", registryDockerURL(t, srv, "private/foo"),
		"--tls-verify=false",
		"--no-creds",
	)
	require.Equalf(t, 3, exit, "expected exit 3 (env): stdout=%s stderr=%s", stdout, stderr)
	require.Containsf(t, stdout, "network", "expected network check: %s", stdout)
	require.Containsf(t, strings.ToLower(stdout), "unauthorized", "expected auth failure: %s", stdout)
}

// TestDoctorProbe_FailOnManifestMissing asserts a probe against a
// non-existent tag produces a manifest-missing classification.
func TestDoctorProbe_FailOnManifestMissing(t *testing.T) {
	bin := integrationBinary(t)
	srv := registrytest.New(t)

	stdout, stderr, exit := runDiffahBin(t, bin,
		"doctor",
		"--probe", registryDockerURL(t, srv, "does-not-exist/at-all"),
		"--tls-verify=false",
	)
	require.Equalf(t, 3, exit, "expected exit 3: stdout=%s stderr=%s", stdout, stderr)
	require.Containsf(t, stdout, "network", "expected network check: %s", stdout)
	out := strings.ToLower(stdout)
	require.Truef(t,
		strings.Contains(out, "manifest unknown") ||
			strings.Contains(out, "name unknown") ||
			strings.Contains(out, "not found"),
		"expected manifest-missing classification: %s", stdout)
}

// TestDoctorProbe_TimeoutOnBlackHole asserts that a registry that
// accepts the TCP connection but never returns a response causes the
// network check to fail within ~15 s, not hang indefinitely.
//
// The black-hole listener calls Accept and holds connections open
// without writing. Go's HTTP client will block on the read, and our
// context.WithTimeout(probeTimeout) must cancel the request.
func TestDoctorProbe_TimeoutOnBlackHole(t *testing.T) {
	bin := integrationBinary(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// Drain reads forever; never write a response.
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	addr := ln.Addr().String()
	probe := "docker://" + addr + "/foo:tag"

	start := time.Now()
	stdout, stderr, exit := runDiffahBin(t, bin,
		"doctor",
		"--probe", probe,
		"--tls-verify=false",
	)
	elapsed := time.Since(start)

	require.Equalf(t, 3, exit, "expected exit 3: stdout=%s stderr=%s", stdout, stderr)
	require.Lessf(t, elapsed, 25*time.Second,
		"doctor took %s — must abort within ~15 s + grace", elapsed)
	require.Containsf(t, stdout, "network", "expected network check: %s", stdout)
	out := strings.ToLower(stdout)
	require.Truef(t,
		strings.Contains(out, "deadline") ||
			strings.Contains(out, "context") ||
			strings.Contains(out, "timeout") ||
			strings.Contains(out, "canceled"),
		"expected timeout/deadline classification: %s", stdout)
}
```

Note on the lower bound: deliberately *no* `>= 14s` assertion, only `< 25s`. Some HTTP clients may bail at a lower socket-level deadline (e.g., 10 s), and that's still a passing outcome — the test's purpose is to lock "doctor does not hang", not "doctor uses exactly 15 s". If you observe `elapsed` consistently `< 5s`, investigate whether the context is reaching the dial/read paths; if it's a deterministic libc/OS interaction, document it in a code comment.

- [ ] **Step 3: Run integration tests**

```bash
go test -tags integration -run TestDoctorProbe -v -count=1 ./cmd/...
```

Expected: PASS for all four cases. Expect ~15–20 s total wall time (the black-hole case dominates).

- [ ] **Step 4: Lint**

```bash
golangci-lint run ./cmd/...
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/doctor_integration_test.go
git commit -m "test(doctor): integration — --probe against registrytest

Four cases lock the --probe contract:

  1. Seeded registry, valid manifest → exit 0, network=ok
  2. Basic-auth registry, --no-creds → exit 3, 'unauthorized'
  3. Empty registry, missing tag → exit 3, 'manifest unknown'
  4. Black-hole listener (accept-but-never-respond) → exit 3,
     bounded under 25s (the 15s context timeout fires)

The black-hole case is the timeout-invariant lock for D3:
a hung registry must not hang the diagnostic tool.

Refs: docs/superpowers/plans/2026-04-29-phase5-doctor.md Task 9"
```

---

## Phase 4 — Docs & ship

### Task 10: CHANGELOG entry

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Read the existing `[Unreleased]` section**

```bash
head -100 CHANGELOG.md
```

PR-1 already created the Phase 5 unreleased section with the config-file additions. Append to that section — do not create a duplicate Phase 5 block.

- [ ] **Step 2: Append to the existing `[Unreleased] — Phase 5: DX & diagnostics polish` block**

Inside the existing `### Additions` list, append:

```markdown
- **`diffah doctor` expansion** (Phase 5.1): four new checks alongside the
  existing `zstd` check.
  - `tmpdir` — writes a 1 KiB probe into `$TMPDIR` (or `os.TempDir()`).
  - `authfile` — walks the standard containers-image lookup chain
    (`$REGISTRY_AUTH_FILE` → `$XDG_RUNTIME_DIR/containers/auth.json` →
    `$HOME/.docker/config.json`) and validates the resolved file parses
    as JSON with an `auths` map. Warns (does not fail) when no file
    is found — anonymous pulls still work.
  - `network` — gated by `--probe REF`; round-trips a single
    `GetManifest` against the supplied registry reference under a 15 s
    hard timeout. Skipped when `--probe` is absent.
  - `config` — calls `pkg/config.Validate` against the resolved config
    file. Doctor is exempted from the persistent config-load hook so
    it can diagnose a malformed file structurally instead of
    hard-failing in `PersistentPreRunE`.
- `diffah doctor` accepts the full registry-flag block
  (`--authfile`, `--tls-verify`, `--cert-dir`, `--creds`, …) so
  `--probe` can target private registries with custom credentials and
  TLS.
```

Inside the existing `### Behavior changes` subsection, append:

```markdown
- `diffah doctor` exits 3 (`CategoryEnvironment`) whenever any check
  fails. Previously only the `zstd` check could fail; this is a
  deliberate strengthening (spec G3). Warnings (`warn` status, e.g.,
  authfile lookup chain empty) do not affect the exit code.
```

Inside the existing `### Backward compatibility` subsection, append:

```markdown
- Existing `zstd` check semantics are preserved unchanged. JSON output
  (`--format=json`) keeps its envelope; the `checks` array now contains
  five entries instead of one. Old consumers that filter for
  `"name":"zstd"` continue to work.
```

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): Phase 5.1 doctor expansion

Refs: docs/superpowers/plans/2026-04-29-phase5-doctor.md Task 10"
```

### Task 11: Final smoke + push + PR

**Files:**
- (none)

- [ ] **Step 1: Full quality gate**

```bash
go build ./...
go test ./... -count=1
go test -tags integration ./... -count=1
go vet ./...
golangci-lint run ./...
```

All must pass. The integration suite includes Task 9's four `TestDoctorProbe_*` cases plus the existing apply/diff/bundle integration tests; expect ~5–10 minutes wall time depending on machine.

- [ ] **Step 2: Push branch**

```bash
git push -u origin spec/phase5-doctor
```

- [ ] **Step 3: Open PR**

```bash
gh pr create --base master --head spec/phase5-doctor \
  --title "feat(doctor): authfile / tmpdir / network / config checks (Phase 5.1)" \
  --body "$(cat <<'EOF'
## Summary

Phase 5.1 of the production-readiness roadmap. Expands `diffah doctor`
from one check (`zstd`) to five (`zstd`, `tmpdir`, `authfile`,
`network`, `config`).

- **tmpdir** — 1 KiB write probe against `$TMPDIR` / `os.TempDir()`.
- **authfile** — `imageio.ResolveAuthFile()` lookup chain + JSON parse.
- **network** — `--probe REF` round-trips a single `GetManifest` under
  a 15 s hard timeout. Skipped when `--probe` is absent.
- **config** — `pkg/config.Validate` against the resolved config file.
  Doctor is exempted from the persistent config-load hook so it can
  diagnose a malformed config structurally.

The doctor command also gains the full registry-flag block
(`--authfile`, `--tls-verify`, `--cert-dir`, `--creds`, …) so
`--probe` can target private registries.

## Test plan

- [x] `go test ./cmd/ -run TestDoctor`, `TestTmpdirCheck`,
      `TestAuthfileCheck`, `TestNetworkCheck`, `TestConfigCheck` (unit)
- [x] `go test -tags integration -run TestDoctorProbe ./cmd/...`
      (four registrytest-backed cases including a 15 s black-hole
      timeout invariant)
- [x] `go vet ./...`, `golangci-lint run ./...`

## Coupling notes

- `internal/imageio.defaultAuthFile` was renamed and exported as
  `ResolveAuthFile`. Single-file call-site rewrite in the same package.
- `cmd/root.go`'s `isConfigSubtree` predicate was renamed
  `isExemptFromConfigLoad` and now also exempts `doctor`.

Refs: \`docs/superpowers/specs/2026-04-29-phase5-dx-polish-design.md\` §6
Refs: \`docs/superpowers/plans/2026-04-29-phase5-doctor.md\`
EOF
)"
```

- [ ] **Step 4: Watch CI (the canonical lint gate, per the version-mismatch lesson from PR-1)**

```bash
gh pr checks <PR-NUMBER> --watch --interval 30
```

CI runs `golangci-lint v2.11.4` which is stricter than typical local installs (PR-1's session caught a `gosec G703` discrepancy). If CI fails locally-passing lint, **do not** disable the rule blindly — investigate the specific finding and either fix the code or apply a narrowly-scoped `//nolint:` directive with justification (see PR-1's `internal/imageio/sysctx.go:155` for the pattern).

If green:

```bash
gh pr merge <PR-NUMBER> --squash --delete-branch
```

If failures: investigate locally with `go test -count=1` / `golangci-lint run`, fix, push a follow-up commit (do not amend), repeat.

---

## Self-review checklist

(Run before declaring complete.)

**Spec coverage** (`docs/superpowers/specs/2026-04-29-phase5-dx-polish-design.md` §6):

- §6.1 Check set (5 checks) → Tasks 5/6/7/8 (tmpdir, authfile, network, config) + existing zstd ✔
- §6.2 authfile lookup chain → Task 2 (export) + Task 6 (consume) ✔
- §6.3 `--probe` semantics → Task 7 (skip + sysctx + ParseReference + 15 s timeout + ClassifyRegistryErr) ✔
- §6.4 Exit-code mapping → no change needed; existing `errDoctorChecksFailed` already returns `CategoryEnvironment` (exit 3); strengthened by adding 4 more failable checks ✔
- §6.5 File layout (5 files) → all five touched per the file plan above ✔
- §9 Backward compatibility (zstd preserved, JSON envelope unchanged, exit-code semantics noted) → Task 10 CHANGELOG ✔

**Brainstorm decisions:**
- D1 isExemptFromConfigLoad → Task 3 ✔
- D2 ResolveAuthFile export → Task 2 ✔
- D3 15 s timeout + ClassifyRegistryErr → Task 7 + Task 9 black-hole test ✔
- D4 full installRegistryFlags + doctor-local --probe + defaultChecks(probe, buildSysCtx) → Task 4 ✔

**Placeholder scan:** none. Every step shows complete code.

**Type consistency:**
- `Check` interface: `Name() string`, `Run(ctx context.Context) CheckResult` — unchanged ✔
- `defaultChecks(probe string, buildSysCtx registryContextBuilder) []Check` — used identically in Task 4 (impl), Task 4 Step 3 (test), and as the consumer in Task 8 ✔
- `tmpdirCheck`, `authfileCheck`, `networkCheck`, `configCheck` — names match between `cmd/doctor_checks.go`, internal tests, and the `defaultChecks` slice ✔
- `probeTimeout = 15 * time.Second` — defined once in `cmd/doctor_checks.go` (Task 7), referenced in Task 9 black-hole test only as a comment (the test asserts `< 25s`, not the exact constant) ✔
- `registryContextBuilder` — already exists at `cmd/registry_flags.go:17` as `func() (*types.SystemContext, int, time.Duration, error)`; networkCheck stores and calls it identically ✔

**Open issues to verify during execution:**

- Task 9's black-hole timing assertion is intentionally loose (`< 25 s` only). If the test passes far below 5 s on every machine, the underlying HTTP client may be enforcing its own dial deadline rather than honouring the context — that's still acceptable behaviour (doctor doesn't hang) but consider documenting it in a code comment so future maintainers don't tighten the bound and break it.
- `cmd/doctor_test.go`'s `containsStr` helper may become unused after Task 8 Step 6's edit; if `go vet` or lint flags it, delete it.
- `cmd/doctor_internal_test.go` will accumulate ~12 new tests; the file should remain readable. If it crosses 300 lines, split into `doctor_checks_test.go` (also internal package). YAGNI for now — split only if asked.
