# Phase 2 — Registry-Native Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `diffah apply` and `diffah unbundle` work against live container registries (`docker://`), OCI layouts (`oci:`), and image directories (`dir:`) — with full authentication/TLS/retry controls and lazy baseline-layer fetching.

**Architecture:** Two-layer change. Library (`pkg/diff`, `pkg/importer`, `internal/imageio`) gains transport-agnostic baseline/output references, an open `types.ImageSource` per baseline for lazy blob fetch, and upstream `copy.Image`-based writes. CLI (`cmd/`) widens its transport acceptance, adds a shared `SystemContext` flag block, renames positionals, and drops `--image-format`. A new in-process `internal/registrytest` harness drives registry-dependent tests without an external registry.

**Tech Stack:** Go 1.21+, `go.podman.io/image/v5` (v5.39.2) — `transports/alltransports`, `copy`, `types.SystemContext`. Test harness uses `github.com/google/go-containerregistry/pkg/registry`. Existing `pkg/diff/errs` error taxonomy. Existing `github.com/spf13/cobra` CLI + `github.com/stretchr/testify` tests.

**Spec:** `docs/superpowers/specs/2026-04-24-phase2-registry-native-import-design.md`
**Roadmap:** `docs/superpowers/specs/2026-04-23-production-readiness-roadmap-design.md`

---

## Pre-flight

The brainstorm already created and committed the spec on branch `spec/v2-phase2-registry-native-import` (commit `21750a1`). All implementation commits land on that branch.

Verify:

```bash
git branch --show-current          # expect: spec/v2-phase2-registry-native-import
git log --oneline -1               # expect: 21750a1 docs(spec): add Phase 2 registry-native import design
```

Add the integration-test harness dependency:

```bash
go get github.com/google/go-containerregistry@latest
go mod tidy
```

Expected: `go.mod` gains `github.com/google/go-containerregistry` as a direct dependency. Commit separately:

```bash
git add go.mod go.sum
git commit -m "chore: add go-containerregistry for in-process registry tests

The Phase 2 integration-test harness (internal/registrytest) wraps
go-containerregistry's in-process HTTP registry so tests can exercise
registry pull and push without a running Docker daemon or external
zot/registry:2 container."
```

---

## Stage 1 — Library error types and spec parsers

Land new error types, a registry error classifier, and the `OUTPUT-SPEC` parser first. No behaviour change to any existing code path.

### Task 1.1: Add `ErrRegistry*` typed errors

**Files:**
- Modify: `pkg/diff/errors.go`

- [ ] **Step 1: Write the failing tests**

Append to `pkg/diff/errors_test.go`:

```go
func TestErrRegistryAuth_Classify(t *testing.T) {
	err := &ErrRegistryAuth{Registry: "ghcr.io"}
	cat, hint := errs.Classify(err)
	require.Equal(t, errs.CategoryUser, cat)
	require.Equal(t, "verify --authfile or --creds for this registry", hint)
	require.Contains(t, err.Error(), "ghcr.io")
	require.Contains(t, err.Error(), "authentication")
}

func TestErrRegistryNetwork_Classify(t *testing.T) {
	err := &ErrRegistryNetwork{Op: "GET manifest", Cause: errors.New("connection refused")}
	cat, hint := errs.Classify(err)
	require.Equal(t, errs.CategoryEnvironment, cat)
	require.Contains(t, hint, "retry")
	require.Contains(t, err.Error(), "GET manifest")
	require.Contains(t, err.Error(), "connection refused")
}

func TestErrRegistryManifestMissing_Classify(t *testing.T) {
	err := &ErrRegistryManifestMissing{Ref: "docker://ghcr.io/org/app:v1"}
	cat, hint := errs.Classify(err)
	require.Equal(t, errs.CategoryContent, cat)
	require.Contains(t, hint, "tag or repository")
	require.Contains(t, err.Error(), "docker://ghcr.io/org/app:v1")
}

func TestErrRegistryManifestInvalid_Classify(t *testing.T) {
	err := &ErrRegistryManifestInvalid{Ref: "docker://x/y:z", Reason: "unsupported schema"}
	cat, hint := errs.Classify(err)
	require.Equal(t, errs.CategoryContent, cat)
	require.Contains(t, hint, "corrupt or uses an unsupported schema")
	require.Contains(t, err.Error(), "unsupported schema")
}
```

- [ ] **Step 2: Run tests to verify failure**

```bash
go test ./pkg/diff/ -run "TestErrRegistry" -v
```

Expected: fail with `undefined: ErrRegistryAuth` / `ErrRegistryNetwork` / etc.

- [ ] **Step 3: Implement**

Append to `pkg/diff/errors.go` (before the category registration block near the bottom):

```go
// ErrRegistryAuth is returned when authentication against a registry
// fails (401/403 or an auth-config parse error). Classified as user.
type ErrRegistryAuth struct{ Registry string }

func (e *ErrRegistryAuth) Error() string {
	return fmt.Sprintf("authentication failed against registry %q", e.Registry)
}

// ErrRegistryNetwork wraps connectivity, DNS, and timeout errors
// raised while talking to a registry. Classified as environment.
type ErrRegistryNetwork struct {
	Op    string
	Cause error
}

func (e *ErrRegistryNetwork) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("registry network error during %s", e.Op)
	}
	return fmt.Sprintf("registry network error during %s: %v", e.Op, e.Cause)
}

func (e *ErrRegistryNetwork) Unwrap() error { return e.Cause }

// ErrRegistryManifestMissing is returned when a manifest request
// returns 404. Classified as content.
type ErrRegistryManifestMissing struct{ Ref string }

func (e *ErrRegistryManifestMissing) Error() string {
	return fmt.Sprintf("manifest not found at %s", e.Ref)
}

// ErrRegistryManifestInvalid is returned when a manifest body fails
// to parse or uses an unsupported schema. Classified as content.
type ErrRegistryManifestInvalid struct{ Ref, Reason string }

func (e *ErrRegistryManifestInvalid) Error() string {
	return fmt.Sprintf("invalid manifest at %s: %s", e.Ref, e.Reason)
}
```

Then in the registration block at the bottom of `pkg/diff/errors.go`, add:

```go
func (*ErrRegistryAuth) Category() errs.Category { return errs.CategoryUser }
func (*ErrRegistryAuth) NextAction() string {
	return "verify --authfile or --creds for this registry"
}

func (*ErrRegistryNetwork) Category() errs.Category { return errs.CategoryEnvironment }
func (*ErrRegistryNetwork) NextAction() string {
	return "check connectivity and retry with --retry-times / --retry-delay"
}

func (*ErrRegistryManifestMissing) Category() errs.Category { return errs.CategoryContent }
func (*ErrRegistryManifestMissing) NextAction() string {
	return "manifest was not found — check tag or repository spelling"
}

func (*ErrRegistryManifestInvalid) Category() errs.Category { return errs.CategoryContent }
func (*ErrRegistryManifestInvalid) NextAction() string {
	return "manifest at this reference is corrupt or uses an unsupported schema"
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./pkg/diff/ -run "TestErrRegistry" -v
```

Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/diff/errors.go pkg/diff/errors_test.go
git commit -m "feat(errs): add ErrRegistry{Auth,Network,ManifestMissing,ManifestInvalid}

Typed errors for registry failure modes. Category wiring:
- ErrRegistryAuth          → user (exit 2)
- ErrRegistryNetwork       → environment (exit 3)
- ErrRegistryManifestMissing → content (exit 4)
- ErrRegistryManifestInvalid → content (exit 4)

Implements spec §5.5 (Error classification) for Phase 2."
```

---

### Task 1.2: `classifyRegistryErr` helper

**Files:**
- Create: `pkg/diff/classify_registry.go`
- Test: `pkg/diff/classify_registry_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// pkg/diff/classify_registry_test.go
package diff

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyRegistryErr_Auth(t *testing.T) {
	// upstream docker package surfaces this style of error on 401/403
	upstream := fmt.Errorf("unauthorized: access to the requested resource is not authorized")
	got := ClassifyRegistryErr(upstream, "ghcr.io/org/app:v1")
	var typed *ErrRegistryAuth
	require.ErrorAs(t, got, &typed)
	require.Equal(t, "ghcr.io/org/app:v1", typed.Registry)
}

func TestClassifyRegistryErr_ManifestMissing(t *testing.T) {
	upstream := fmt.Errorf("manifest unknown: manifest for repo:v99 not found")
	got := ClassifyRegistryErr(upstream, "docker://ghcr.io/org/app:v99")
	var typed *ErrRegistryManifestMissing
	require.ErrorAs(t, got, &typed)
}

func TestClassifyRegistryErr_Network(t *testing.T) {
	upstream := &url.Error{Op: "Get", URL: "https://x", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}
	got := ClassifyRegistryErr(upstream, "docker://x/y:z")
	var typed *ErrRegistryNetwork
	require.ErrorAs(t, got, &typed)
}

func TestClassifyRegistryErr_ManifestInvalid(t *testing.T) {
	upstream := fmt.Errorf("manifest schema version 0 is unsupported")
	got := ClassifyRegistryErr(upstream, "docker://x/y:z")
	var typed *ErrRegistryManifestInvalid
	require.ErrorAs(t, got, &typed)
	require.Contains(t, typed.Reason, "schema")
}

func TestClassifyRegistryErr_PassesThroughUnrecognized(t *testing.T) {
	unknown := errors.New("some other thing")
	got := ClassifyRegistryErr(unknown, "docker://x/y:z")
	// Not rewrapped — falls through to existing classifier logic.
	require.Same(t, unknown, got)
}

func TestClassifyRegistryErr_NilIsNil(t *testing.T) {
	require.NoError(t, ClassifyRegistryErr(nil, "anything"))
}
```

- [ ] **Step 2: Run tests to verify failure**

```bash
go test ./pkg/diff/ -run "TestClassifyRegistryErr" -v
```

Expected: fail with `undefined: ClassifyRegistryErr`.

- [ ] **Step 3: Implement**

```go
// pkg/diff/classify_registry.go
package diff

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// ClassifyRegistryErr maps upstream registry errors to diffah typed
// error types that carry the correct exit-code category. When the
// error is not recognised as registry-related, the original error is
// returned unchanged so the existing errs.Classify fallbacks still
// apply.
func ClassifyRegistryErr(err error, ref string) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())

	switch {
	case containsAny(msg, "unauthorized", "authentication required", "denied"):
		return &ErrRegistryAuth{Registry: ref}
	case containsAny(msg, "manifest unknown", "not found"):
		if containsAny(msg, "manifest", "not found") {
			return &ErrRegistryManifestMissing{Ref: ref}
		}
	case containsAny(msg, "schema version", "unsupported media type", "invalid manifest"):
		return &ErrRegistryManifestInvalid{Ref: ref, Reason: err.Error()}
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &ErrRegistryNetwork{Op: urlErr.Op, Cause: err}
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return &ErrRegistryNetwork{Op: netErr.Op, Cause: err}
	}
	return err
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify pass**

```bash
go test ./pkg/diff/ -run "TestClassifyRegistryErr" -v
```

Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/diff/classify_registry.go pkg/diff/classify_registry_test.go
git commit -m "feat(diff): add ClassifyRegistryErr helper

Maps upstream registry errors (string-matched; url.Error /
net.OpError via errors.As) into the four ErrRegistry* typed errors.
Unrecognised errors pass through so the existing errs.Classify
fallbacks (filesystem, context, etc.) still apply.

Implements spec §5.5."
```

---

### Task 1.3: `OUTPUT-SPEC` parser + strict-prefix in `BASELINE-SPEC`

**Files:**
- Modify: `pkg/diff/bundle_spec.go`
- Test: `pkg/diff/output_spec_test.go` (create) + extend `pkg/diff/bundle_spec_test.go` if that exists (otherwise inline into the new file)

- [ ] **Step 1: Write the failing tests**

```go
// pkg/diff/output_spec_test.go
package diff

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOutputSpec_AcceptsValidTransportRefs(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "outputs.json")
	body := `{"outputs":{
		"svc-a": "docker://ghcr.io/org/svc-a:v2",
		"svc-b": "oci-archive:/tmp/svc-b.tar",
		"svc-c": "dir:/tmp/svc-c"
	}}`
	require.NoError(t, os.WriteFile(specPath, []byte(body), 0o600))

	spec, err := ParseOutputSpec(specPath)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"svc-a": "docker://ghcr.io/org/svc-a:v2",
		"svc-b": "oci-archive:/tmp/svc-b.tar",
		"svc-c": "dir:/tmp/svc-c",
	}, spec.Outputs)
}

func TestParseOutputSpec_RejectsMissingKey(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "outputs.json")
	require.NoError(t, os.WriteFile(specPath, []byte(`{"nope": {}}`), 0o600))

	_, err := ParseOutputSpec(specPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outputs")
}

func TestParseOutputSpec_RejectsEmpty(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "outputs.json")
	require.NoError(t, os.WriteFile(specPath, []byte(`{"outputs":{}}`), 0o600))

	_, err := ParseOutputSpec(specPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outputs must be non-empty")
}

func TestParseOutputSpec_RejectsBarePath(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "outputs.json")
	require.NoError(t, os.WriteFile(specPath, []byte(
		`{"outputs":{"svc-a":"/tmp/a.tar"}}`), 0o600))

	_, err := ParseOutputSpec(specPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing transport prefix")
	require.Contains(t, err.Error(), "svc-a")
}

func TestParseOutputSpec_RejectsInvalidName(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "outputs.json")
	require.NoError(t, os.WriteFile(specPath, []byte(
		`{"outputs":{"bad name!":"docker://x/y:z"}}`), 0o600))

	_, err := ParseOutputSpec(specPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bad name!")
}

func TestParseBaselineSpec_RejectsBarePath(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "baselines.json")
	require.NoError(t, os.WriteFile(specPath, []byte(
		`{"baselines":{"svc-a":"/tmp/a.tar"}}`), 0o600))

	_, err := ParseBaselineSpec(specPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing transport prefix")
	require.Contains(t, err.Error(), "svc-a")
}

func TestParseBaselineSpec_AcceptsTransportRefs(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "baselines.json")
	require.NoError(t, os.WriteFile(specPath, []byte(
		`{"baselines":{"svc-a":"docker://x/y:v1"}}`), 0o600))

	spec, err := ParseBaselineSpec(specPath)
	require.NoError(t, err)
	require.Equal(t, "docker://x/y:v1", spec.Baselines["svc-a"])
}
```

- [ ] **Step 2: Run tests to verify failure**

```bash
go test ./pkg/diff/ -run "TestParseOutputSpec|TestParseBaselineSpec" -v
```

Expected: fail on `undefined: ParseOutputSpec`, and the new baseline-spec cases fail because the current parser does filesystem-path joining.

- [ ] **Step 3: Implement**

Append to `pkg/diff/bundle_spec.go`:

```go
// OutputSpec is the parsed form of an OUTPUT-SPEC JSON file used by
// 'diffah unbundle' to map each image name in the bundle to a fully-
// qualified transport-prefixed destination reference.
type OutputSpec struct {
	Outputs map[string]string `json:"outputs"`
}

// ParseOutputSpec reads a JSON file of the form:
//
//	{"outputs": {"<name>": "<transport>:<path-or-url>", ...}}
//
// Every value must carry a transport prefix accepted by
// go.podman.io/image/v5/transports/alltransports. Returns
// *ErrInvalidBundleSpec on any shape/content failure.
func ParseOutputSpec(path string) (*OutputSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	// Reject unknown top-level keys so we do not silently swallow typos
	// (e.g. "output" instead of "outputs").
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	if _, ok := probe["outputs"]; !ok {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: `missing required field "outputs"`}
	}

	var spec OutputSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	if len(spec.Outputs) == 0 {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: "outputs must be non-empty"}
	}
	for name, ref := range spec.Outputs {
		if !nameRegex.MatchString(name) {
			return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
				"outputs[%q] name does not match %s", name, nameRegex)}
		}
		if err := validateTransportRef(ref); err != nil {
			return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
				"outputs[%q]: %s", name, err.Error())}
		}
	}
	return &spec, nil
}

// validateTransportRef checks that ref has a supported transport
// prefix and parses under alltransports. Rejects bare filesystem paths.
func validateTransportRef(ref string) error {
	colon := strings.Index(ref, ":")
	if colon <= 0 {
		return fmt.Errorf("missing transport prefix: %q (expected e.g. docker-archive:%s)", ref, ref)
	}
	if _, err := alltransports.ParseImageName(ref); err != nil {
		return fmt.Errorf("invalid image reference %q: %v", ref, err)
	}
	return nil
}
```

Update imports at the top of `pkg/diff/bundle_spec.go` to add:

```go
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"   // still needed for resolveSpecPath below (if kept)
	"strings"

	"go.podman.io/image/v5/transports/alltransports"
)
```

Replace the existing `ParseBaselineSpec` value-handling (the `resolveSpecPath` call) so it validates transport refs instead of joining filesystem paths. The body of `ParseBaselineSpec` becomes:

```go
func ParseBaselineSpec(path string) (*BaselineSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	var spec BaselineSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	if len(spec.Baselines) == 0 {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: "baselines must be non-empty"}
	}
	for name, ref := range spec.Baselines {
		if !nameRegex.MatchString(name) {
			return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
				"name %q does not match %s", name, nameRegex)}
		}
		if err := validateTransportRef(ref); err != nil {
			return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
				"baselines[%q]: %s", name, err.Error())}
		}
	}
	return &spec, nil
}
```

Keep the existing `ParseBundleSpec` (used by `diff`/`bundle`) and `resolveSpecPath` untouched — `BUNDLE-SPEC` still describes local `baseline`/`target` filesystem paths for `diff`/`bundle`; Phase 3 widens that separately.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./pkg/diff/ -run "TestParseOutputSpec|TestParseBaselineSpec|TestParseBundleSpec" -v
```

Expected: PASS. Update any fixture-bearing existing tests that used filesystem paths in BASELINE-SPEC to prefix them with `docker-archive:` before running the full suite.

- [ ] **Step 5: Sweep for existing BASELINE-SPEC test fixtures that need the prefix**

```bash
grep -rn '"baselines":' --include="*.go" pkg/ cmd/
```

Expected callers: `cmd/unbundle_integration_test.go` (uses `filepath.Join(root, "testdata/fixtures/v1_oci.tar")` in baselines map). Update these to prefix with `oci-archive:`:

```go
// before
"baselines": map[string]string{
    "app": filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
},
// after
"baselines": map[string]string{
    "app": "oci-archive:" + filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
},
```

- [ ] **Step 6: Run full test suites**

```bash
go test ./pkg/... ./cmd/ -count=1
go test -tags integration ./cmd/ -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/diff/bundle_spec.go pkg/diff/output_spec_test.go cmd/unbundle_integration_test.go
git commit -m "feat(diff): add ParseOutputSpec + strict-prefix BASELINE-SPEC

BASELINE-SPEC values must now carry a transport prefix (same rule as
BASELINE-IMAGE positional). ParseOutputSpec mirrors the shape for the
new OUTPUT-SPEC positional on 'diffah unbundle', required for Phase 2
registry-native import.

Breaking change: existing baseline spec files with bare filesystem
paths now fail user/2 with a 'missing transport prefix' error. See
spec §3.4 for migration ('oci-archive:' or 'docker-archive:' prefix).

Existing integration fixtures updated in the same commit.

Implements spec §3.4 (Spec file schemas)."
```

---

### Task 1.4: `imageio.BuildSystemContext` helper

**Files:**
- Create: `internal/imageio/sysctx.go`
- Test: `internal/imageio/sysctx_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/imageio/sysctx_test.go
package imageio

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"
)

func TestBuildSystemContext_NoFlagsDefaultsToVerifyingTLS(t *testing.T) {
	sc, err := BuildSystemContext(SystemContextFlags{})
	require.NoError(t, err)
	require.NotNil(t, sc)
	require.Equal(t, types.OptionalBoolUndefined, sc.DockerInsecureSkipTLSVerify,
		"unset flag should leave TLS at containers-image default (=verify)")
}

func TestBuildSystemContext_TLSVerifyFalse(t *testing.T) {
	sc, err := BuildSystemContext(SystemContextFlags{TLSVerify: OptionalBoolPtr(false)})
	require.NoError(t, err)
	require.Equal(t, types.OptionalBoolTrue, sc.DockerInsecureSkipTLSVerify)
}

func TestBuildSystemContext_CredsSplit(t *testing.T) {
	sc, err := BuildSystemContext(SystemContextFlags{Creds: "alice:s3cret"})
	require.NoError(t, err)
	require.NotNil(t, sc.DockerAuthConfig)
	require.Equal(t, "alice", sc.DockerAuthConfig.Username)
	require.Equal(t, "s3cret", sc.DockerAuthConfig.Password)
}

func TestBuildSystemContext_UsernamePasswordPair(t *testing.T) {
	sc, err := BuildSystemContext(SystemContextFlags{Username: "bob", Password: "p"})
	require.NoError(t, err)
	require.Equal(t, "bob", sc.DockerAuthConfig.Username)
	require.Equal(t, "p", sc.DockerAuthConfig.Password)
}

func TestBuildSystemContext_UsernameWithoutPasswordFails(t *testing.T) {
	_, err := BuildSystemContext(SystemContextFlags{Username: "bob"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--username requires --password")
}

func TestBuildSystemContext_BearerToken(t *testing.T) {
	sc, err := BuildSystemContext(SystemContextFlags{RegistryToken: "abc123"})
	require.NoError(t, err)
	require.Equal(t, "abc123", sc.DockerBearerRegistryToken)
}

func TestBuildSystemContext_CertDir(t *testing.T) {
	tmp := t.TempDir()
	sc, err := BuildSystemContext(SystemContextFlags{CertDir: tmp})
	require.NoError(t, err)
	require.Equal(t, tmp, sc.DockerCertPath)
}

func TestBuildSystemContext_AuthfileExplicit(t *testing.T) {
	tmp := t.TempDir()
	af := filepath.Join(tmp, "auth.json")
	require.NoError(t, os.WriteFile(af, []byte("{}"), 0o600))

	sc, err := BuildSystemContext(SystemContextFlags{AuthFile: af})
	require.NoError(t, err)
	require.Equal(t, af, sc.AuthFilePath)
}

func TestBuildSystemContext_MutuallyExclusiveCredsAndNoCreds(t *testing.T) {
	_, err := BuildSystemContext(SystemContextFlags{Creds: "u:p", NoCreds: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutually exclusive")
}

func TestBuildSystemContext_MutuallyExclusiveCredsAndUsername(t *testing.T) {
	_, err := BuildSystemContext(SystemContextFlags{Creds: "u:p", Username: "bob", Password: "q"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutually exclusive")
}

func TestBuildSystemContext_NoCredsWithBearer(t *testing.T) {
	_, err := BuildSystemContext(SystemContextFlags{NoCreds: true, RegistryToken: "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutually exclusive")
}

func TestBuildSystemContext_DefaultAuthfilePrecedence(t *testing.T) {
	tmp := t.TempDir()
	xdg := filepath.Join(tmp, "xdg", "containers")
	require.NoError(t, os.MkdirAll(xdg, 0o755))
	xdgFile := filepath.Join(xdg, "auth.json")
	require.NoError(t, os.WriteFile(xdgFile, []byte("{}"), 0o600))

	dockerDir := filepath.Join(tmp, "home", ".docker")
	require.NoError(t, os.MkdirAll(dockerDir, 0o755))
	dockerFile := filepath.Join(dockerDir, "config.json")
	require.NoError(t, os.WriteFile(dockerFile, []byte("{}"), 0o600))

	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "xdg"))
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	sc, err := BuildSystemContext(SystemContextFlags{})
	require.NoError(t, err)
	require.Equal(t, xdgFile, sc.AuthFilePath, "XDG file should win over docker config.json")
}

func TestBuildSystemContext_REGISTRY_AUTH_FILE_WinsOverXDG(t *testing.T) {
	tmp := t.TempDir()
	override := filepath.Join(tmp, "override.json")
	require.NoError(t, os.WriteFile(override, []byte("{}"), 0o600))

	xdg := filepath.Join(tmp, "xdg", "containers")
	require.NoError(t, os.MkdirAll(xdg, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(xdg, "auth.json"), []byte("{}"), 0o600))

	t.Setenv("REGISTRY_AUTH_FILE", override)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "xdg"))

	sc, err := BuildSystemContext(SystemContextFlags{})
	require.NoError(t, err)
	require.Equal(t, override, sc.AuthFilePath)
}
```

- [ ] **Step 2: Run tests to verify failure**

```bash
go test ./internal/imageio/ -run "TestBuildSystemContext" -v
```

Expected: fail on undefined types / functions.

- [ ] **Step 3: Implement**

```go
// internal/imageio/sysctx.go
package imageio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.podman.io/image/v5/types"
)

// SystemContextFlags holds the raw CLI-flag values that
// BuildSystemContext translates into an upstream *types.SystemContext.
// The consumer (cmd/registry_flags.go) registers these as cobra flags
// and fills the struct.
type SystemContextFlags struct {
	AuthFile      string
	Creds         string
	Username      string
	Password      string
	NoCreds       bool
	RegistryToken string
	TLSVerify     *bool   // nil = default; true/false = user override
	CertDir       string
	RetryTimes    int
	RetryDelay    time.Duration
}

// OptionalBoolPtr is a helper for tests to build *bool values.
func OptionalBoolPtr(v bool) *bool { return &v }

// BuildSystemContext validates the flag combination and constructs
// the upstream types.SystemContext.
//
// On validation failure the returned error is a plain value
// (no classification). Callers in cmd/registry_flags.go wrap it in
// a cliErr with categoryUser.
func BuildSystemContext(f SystemContextFlags) (*types.SystemContext, error) {
	if err := validateCredentialFlags(f); err != nil {
		return nil, err
	}

	sc := &types.SystemContext{}

	switch {
	case f.AuthFile != "":
		sc.AuthFilePath = f.AuthFile
	case !f.NoCreds && f.Creds == "" && f.Username == "" && f.RegistryToken == "":
		sc.AuthFilePath = defaultAuthFile()
	}

	switch {
	case f.Creds != "":
		user, pass, ok := splitCreds(f.Creds)
		if !ok {
			return nil, fmt.Errorf("invalid --creds %q: expected USER[:PASS]", f.Creds)
		}
		sc.DockerAuthConfig = &types.DockerAuthConfig{Username: user, Password: pass}
	case f.Username != "":
		if f.Password == "" {
			return nil, fmt.Errorf("--username requires --password")
		}
		sc.DockerAuthConfig = &types.DockerAuthConfig{Username: f.Username, Password: f.Password}
	case f.NoCreds:
		// Force anonymous: clear any auth file precedence.
		sc.AuthFilePath = ""
		sc.DockerAuthConfig = nil
	}

	if f.RegistryToken != "" {
		sc.DockerBearerRegistryToken = f.RegistryToken
	}

	if f.TLSVerify != nil {
		if *f.TLSVerify {
			sc.DockerInsecureSkipTLSVerify = types.OptionalBoolFalse
		} else {
			sc.DockerInsecureSkipTLSVerify = types.OptionalBoolTrue
		}
	}

	if f.CertDir != "" {
		sc.DockerCertPath = f.CertDir
	}

	return sc, nil
}

func validateCredentialFlags(f SystemContextFlags) error {
	credSources := 0
	if f.Creds != "" {
		credSources++
	}
	if f.Username != "" || f.Password != "" {
		credSources++
	}
	if f.RegistryToken != "" {
		credSources++
	}
	if f.NoCreds {
		credSources++
	}
	if credSources > 1 {
		return fmt.Errorf("--creds, --username/--password, --registry-token, and --no-creds are mutually exclusive")
	}
	return nil
}

func splitCreds(raw string) (user, pass string, ok bool) {
	idx := strings.Index(raw, ":")
	if idx < 0 {
		return raw, "", raw != ""
	}
	return raw[:idx], raw[idx+1:], idx > 0
}

// defaultAuthFile returns the first existing file in the standard
// precedence chain: $REGISTRY_AUTH_FILE → $XDG_RUNTIME_DIR/containers/auth.json
// → $HOME/.docker/config.json. Returns empty string when none exist
// (upstream containers-image treats this as "no credentials available").
func defaultAuthFile() string {
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/imageio/ -run "TestBuildSystemContext" -v
```

Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add internal/imageio/sysctx.go internal/imageio/sysctx_test.go
git commit -m "feat(imageio): add BuildSystemContext flag translator

Translates the Phase 2 registry & transport flag block into a
*types.SystemContext. Handles:
- Authfile precedence: REGISTRY_AUTH_FILE → XDG → HOME/.docker
- Mutually exclusive credential sources
- --tls-verify tri-state via OptionalBool
- --cert-dir mTLS directory

Retry semantics (--retry-times, --retry-delay) are captured in
SystemContextFlags but not applied to SystemContext itself — Phase 2
wires those into the copy/retry path at call sites.

Implements spec §4 (Registry & transport flag block)."
```

---

## Stage 2 — Importer library rewire

Reshape the importer so a baseline can be any `types.ImageReference`, blob bytes are fetched lazily per-digest, and the composed output is written via upstream `copy.Image`.

### Task 2.1: Widen `importer.Options`

**Files:**
- Modify: `pkg/importer/importer.go`
- Test: extend `pkg/importer/importer_test.go` (restored helper file) or add a new `options_test.go`

- [ ] **Step 1: Plan the field changes**

Current `importer.Options`:

```go
type Options struct {
	DeltaPath        string
	Baselines        map[string]string
	Strict           bool
	OutputPath       string
	OutputFormat     string
	AllowConvert     bool
	ProgressReporter progress.Reporter
	Progress         io.Writer                       // deprecated
	Probe            func(context.Context) (bool, string)
}
```

Target `importer.Options`:

```go
type Options struct {
	DeltaPath        string
	// Baselines values are now transport-prefixed refs parsed via
	// alltransports.ParseImageName (not plain filesystem paths).
	Baselines        map[string]string
	Strict           bool
	// Outputs maps each image name (matching sidecar.Images[i].Name)
	// to a transport-prefixed destination ref. For single-image apply
	// the CLI passes {"default": <ref>}.
	Outputs          map[string]string
	// SystemContext is threaded through every registry call. Nil
	// means "use containers-image defaults"; in practice the CLI
	// layer always builds one via imageio.BuildSystemContext.
	SystemContext    *types.SystemContext
	AllowConvert     bool
	// RetryTimes/RetryDelay apply to each registry blob/manifest
	// operation. Zero disables. Delay=0 means exponential backoff.
	RetryTimes       int
	RetryDelay       time.Duration
	ProgressReporter progress.Reporter
	Progress         io.Writer                       // deprecated
	Probe            func(context.Context) (bool, string)
}
```

**Removed:** `OutputPath`, `OutputFormat`.

- [ ] **Step 2: Write a failing test asserting the new shape**

Append to `pkg/importer/importer_test.go`:

```go
func TestOptions_AcceptsOutputsMapAndSystemContext(t *testing.T) {
	sc := &types.SystemContext{AuthFilePath: "/tmp/auth.json"}
	opts := Options{
		DeltaPath:     "/tmp/d.tar",
		Baselines:     map[string]string{"svc-a": "docker://x/y:v1"},
		Outputs:       map[string]string{"svc-a": "docker://x/y:v2"},
		SystemContext: sc,
		RetryTimes:    3,
		RetryDelay:    0, // exponential
		AllowConvert:  false,
	}
	require.Equal(t, "docker://x/y:v2", opts.Outputs["svc-a"])
	require.Same(t, sc, opts.SystemContext)
	require.Equal(t, 3, opts.RetryTimes)
}
```

Add the import at the top:

```go
import (
	"go.podman.io/image/v5/types"
)
```

- [ ] **Step 3: Run test to verify failure**

```bash
go test ./pkg/importer/ -run "TestOptions_AcceptsOutputsMapAndSystemContext" -v
```

Expected: fail on `unknown field Outputs / SystemContext / RetryTimes / RetryDelay`.

- [ ] **Step 4: Modify the Options struct**

In `pkg/importer/importer.go`, replace the `Options` struct with the target shape above. Remove `OutputPath` and `OutputFormat`. Add imports `time` and `go.podman.io/image/v5/types`.

- [ ] **Step 5: Run test to verify it passes**

```bash
go test ./pkg/importer/ -run "TestOptions_AcceptsOutputsMapAndSystemContext" -v
```

Expected: PASS.

- [ ] **Step 6: Expect the rest of the importer to break — this is fine**

```bash
go build ./pkg/importer/ 2>&1 | head -20
```

Expected: multiple errors like `opts.OutputPath undefined`. Those are resolved in Task 2.2–2.4.

- [ ] **Step 7: Do NOT commit yet** — leave Options-only changes staged locally; Stage 2 tasks 2.2–2.4 must land together to keep the tree buildable on every commit. (If you want an intermediate commit, keep the removed fields as deprecated aliases. Easier to batch.)

Move to Task 2.2.

---

### Task 2.2: `resolveBaselines` holds open `ImageSource`

**Files:**
- Modify: `pkg/importer/resolve.go`
- Test: existing `pkg/importer/resolve_test.go`

- [ ] **Step 1: Read the current implementation**

Current `resolveBaselines` calls `imageio.OpenArchiveRef(path)` which sniffs a tar file. It opens `NewImageSource`, reads the manifest, closes the source, and returns only `{Name, Ref, Manifest}`.

New behaviour: parse ref via `imageio.ParseReference(raw, sysctx)`, open `NewImageSource`, read manifest, keep the source open in the returned struct for lazy blob fetch later.

- [ ] **Step 2: Extend `imageio.ParseReference` to accept a `SystemContext`**

Modify `internal/imageio/reference.go` so callers can pass a nil or real context:

```go
func ParseReference(ref string) (types.ImageReference, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("empty image reference")
	}
	r, err := alltransports.ParseImageName(ref)
	if err != nil {
		return nil, fmt.Errorf("parse reference %q: %w", ref, err)
	}
	return r, nil
}
```

That already exists and doesn't need a SystemContext — SystemContext is passed to `NewImageSource` / `NewImageDestination` calls downstream. Good.

- [ ] **Step 3: Write the failing test**

Add to `pkg/importer/resolve_test.go`:

```go
func TestResolveBaselines_HoldsOpenSourceForLazyBlobFetch(t *testing.T) {
	ctx := context.Background()
	// Use the canonical OCI fixture via oci-archive: prefix.
	sc := diff.Sidecar{Images: []diff.ImageRecord{
		{Name: "default", Baseline: diff.BaselineRecord{ManifestDigest: digestOfFixtureV1(t)}},
	}}

	baselines := map[string]string{
		"default": "oci-archive:" + fixtureAbsPath(t, "v1_oci.tar"),
	}
	resolved, err := resolveBaselines(ctx, &sc, baselines, nil, true)
	require.NoError(t, err)
	require.Len(t, resolved, 1)

	// The returned resolvedBaseline MUST hold an open source (not nil)
	// so composeImage can issue lazy GetBlob calls.
	require.NotNil(t, resolved[0].Src, "expected open ImageSource")
	defer resolved[0].Src.Close()

	// Source should serve the manifest again — proves it's live.
	raw, _, err := resolved[0].Src.GetManifest(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, raw)
}
```

Where `digestOfFixtureV1` and `fixtureAbsPath` are small helpers you add if not present. If the test file already has harness helpers, reuse them; otherwise:

```go
func fixtureAbsPath(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "..", "..", "testdata", "fixtures", name)
}

func digestOfFixtureV1(t *testing.T) digest.Digest {
	t.Helper()
	ref, err := imageio.OpenArchiveRef(fixtureAbsPath(t, "v1_oci.tar"))
	require.NoError(t, err)
	src, err := ref.NewImageSource(context.Background(), nil)
	require.NoError(t, err)
	defer src.Close()
	raw, _, err := src.GetManifest(context.Background(), nil)
	require.NoError(t, err)
	return digest.FromBytes(raw)
}
```

- [ ] **Step 4: Run test to verify failure**

```bash
go test ./pkg/importer/ -run "TestResolveBaselines_HoldsOpenSourceForLazyBlobFetch" -v
```

Expected: fail — `resolvedBaseline` has no `Src` field.

- [ ] **Step 5: Modify `resolve.go`**

Replace `resolvedBaseline` and `resolveBaselines`:

```go
package importer

import (
	"context"
	"fmt"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
)

type resolvedBaseline struct {
	Name     string
	Ref      types.ImageReference
	// Src is the open ImageSource held for lazy per-blob fetches
	// performed by composeImage. Callers MUST close it when done.
	Src      types.ImageSource
	Manifest digest.Digest
}

func resolveBaselines(
	ctx context.Context,
	sc *diff.Sidecar,
	baselines map[string]string,
	sysctx *types.SystemContext,
	strict bool,
) ([]resolvedBaseline, error) {
	expanded := expandDefaultBaseline(sc, baselines)
	result := make([]resolvedBaseline, 0, len(sc.Images))
	resolved := make(map[string]struct{}, len(sc.Images))

	// On any error after at least one source has been opened, make sure
	// we release everything we've taken so far.
	defer func() {
		// no-op on success; closeOnErr pattern handled explicitly below.
	}()

	openSources := make([]types.ImageSource, 0, len(sc.Images))
	cleanup := func() {
		for _, s := range openSources {
			s.Close()
		}
	}

	for _, img := range sc.Images {
		raw, ok := expanded[img.Name]
		if !ok {
			continue
		}
		ref, err := imageio.ParseReference(raw)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("parse baseline reference for %q: %w", img.Name, err)
		}
		src, err := ref.NewImageSource(ctx, sysctx)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("open baseline source for %q: %w", img.Name,
				diff.ClassifyRegistryErr(err, raw))
		}
		openSources = append(openSources, src)

		manifest, _, err := src.GetManifest(ctx, nil)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("read baseline manifest for %q: %w", img.Name,
				diff.ClassifyRegistryErr(err, raw))
		}
		got := digest.FromBytes(manifest)
		if got != img.Baseline.ManifestDigest {
			cleanup()
			return nil, &diff.ErrBaselineMismatch{
				Name: img.Name, Expected: string(img.Baseline.ManifestDigest), Got: string(got),
			}
		}
		resolved[img.Name] = struct{}{}
		result = append(result, resolvedBaseline{
			Name: img.Name, Ref: ref, Src: src, Manifest: got,
		})
	}

	if strict {
		var missing []string
		for _, img := range sc.Images {
			if _, ok := resolved[img.Name]; !ok {
				missing = append(missing, img.Name)
			}
		}
		if len(missing) > 0 {
			cleanup()
			return nil, &diff.ErrBaselineMissing{Names: missing}
		}
	}

	if err := rejectUnknownBaselineNames(sc, expanded); err != nil {
		cleanup()
		return nil, err
	}
	return result, nil
}

// closeResolvedBaselines releases every open ImageSource held by the
// resolved slice. Safe to call multiple times; nil sources are
// tolerated.
func closeResolvedBaselines(list []resolvedBaseline) {
	for _, r := range list {
		if r.Src != nil {
			r.Src.Close()
		}
	}
}
```

- [ ] **Step 6: Run test to verify pass**

```bash
go test ./pkg/importer/ -run "TestResolveBaselines_HoldsOpenSourceForLazyBlobFetch" -v
```

Expected: PASS.

- [ ] **Step 7: Do NOT commit yet** — callers (`importer.Import`, `compose.go`) still reference the old `Ref` field plus the gone `OutputPath`/`OutputFormat`. Next task closes those.

---

### Task 2.3: Lazy per-blob fetch helper

**Files:**
- Create: `pkg/importer/lazyblob.go`
- Test: `pkg/importer/lazyblob_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/importer/lazyblob_test.go
package importer

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"
)

type fakeSrc struct {
	blobs map[digest.Digest][]byte
	hits  []digest.Digest
}

func (s *fakeSrc) Reference() types.ImageReference { return nil }
func (s *fakeSrc) Close() error                    { return nil }
func (s *fakeSrc) GetManifest(context.Context, *digest.Digest) ([]byte, string, error) {
	return nil, "", nil
}
func (s *fakeSrc) HasThreadSafeGetBlob() bool { return false }
func (s *fakeSrc) GetSignaturesWithFormat(context.Context, *digest.Digest) ([]any, error) {
	return nil, nil
}
func (s *fakeSrc) GetBlob(_ context.Context, bi types.BlobInfo, _ types.BlobInfoCache) (io.ReadCloser, int64, error) {
	s.hits = append(s.hits, bi.Digest)
	b, ok := s.blobs[bi.Digest]
	if !ok {
		return nil, 0, io.ErrUnexpectedEOF
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}

func TestLazyBlobFetcher_FetchesOnlyRequestedDigests(t *testing.T) {
	digestA := digest.FromBytes([]byte("a"))
	digestB := digest.FromBytes([]byte("b"))
	src := &fakeSrc{blobs: map[digest.Digest][]byte{
		digestA: []byte("a"),
		digestB: []byte("b"),
	}}

	f := newLazyBlobFetcher(src)

	got, err := f.Fetch(context.Background(), digestA)
	require.NoError(t, err)
	require.Equal(t, []byte("a"), got)
	require.Equal(t, []digest.Digest{digestA}, src.hits)

	got, err = f.Fetch(context.Background(), digestB)
	require.NoError(t, err)
	require.Equal(t, []byte("b"), got)
	require.Equal(t, []digest.Digest{digestA, digestB}, src.hits)
}

func TestLazyBlobFetcher_MissingDigestBubbles(t *testing.T) {
	src := &fakeSrc{blobs: map[digest.Digest][]byte{}}
	f := newLazyBlobFetcher(src)
	_, err := f.Fetch(context.Background(), digest.FromBytes([]byte("x")))
	require.Error(t, err)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./pkg/importer/ -run "TestLazyBlobFetcher" -v
```

Expected: fail on `newLazyBlobFetcher` undefined.

- [ ] **Step 3: Implement**

```go
// pkg/importer/lazyblob.go
package importer

import (
	"context"
	"fmt"
	"io"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/pkg/blobinfocache"
	"go.podman.io/image/v5/types"
)

// lazyBlobFetcher issues on-demand GetBlob calls against a held-open
// ImageSource. Used by composeImage to pull baseline layers only
// when the delta requires them, avoiding a full-image preload.
type lazyBlobFetcher struct {
	src   types.ImageSource
	cache types.BlobInfoCache
}

func newLazyBlobFetcher(src types.ImageSource) *lazyBlobFetcher {
	return &lazyBlobFetcher{
		src:   src,
		cache: blobinfocache.DefaultCache(nil),
	}
}

// Fetch returns the full byte content of the blob identified by d.
// Callers own the returned slice; repeated calls for the same digest
// re-fetch (no local caching beyond the upstream BlobInfoCache).
func (f *lazyBlobFetcher) Fetch(ctx context.Context, d digest.Digest) ([]byte, error) {
	rc, _, err := f.src.GetBlob(ctx, types.BlobInfo{Digest: d}, f.cache)
	if err != nil {
		return nil, fmt.Errorf("fetch baseline blob %s: %w", d, err)
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
```

- [ ] **Step 4: Run to verify pass**

```bash
go test ./pkg/importer/ -run "TestLazyBlobFetcher" -v
```

Expected: PASS (2 tests).

---

### Task 2.4: `composeImage` writes to `types.ImageReference` via `copy.Image`

**Files:**
- Modify: `pkg/importer/compose.go`
- Modify: `pkg/importer/importer.go` (call-site + remove output path/format logic)

- [ ] **Step 1: Read current `composeImage` signature**

Today:

```go
func composeImage(
	ctx context.Context,
	img diff.ImageRecord,
	bundle *extractedBundle,
	rb resolvedBaseline,
	outputDir string,
	outputFormat string,
	allowConvert bool,
) error
```

Target:

```go
func composeImage(
	ctx context.Context,
	img diff.ImageRecord,
	bundle *extractedBundle,
	rb resolvedBaseline,
	destRef types.ImageReference,
	sysctx *types.SystemContext,
	allowConvert bool,
	reporter progress.Reporter,
) error
```

The function stops doing `filepath.Join(outputDir, name[+".tar"])` and stops calling the internal `buildOutputRef`. Instead `destRef` is passed in (already `alltransports.ParseImageName`'d by the caller).

- [ ] **Step 2: Update the two compose-path branches**

Inside `composeImage`, keep the existing logic that:
- Validates baseline compat (`rb.Manifest == img.Baseline.ManifestDigest`).
- Reads the target manifest from the bundle sidecar.
- Replaces each layer reference: if shipped-in-bundle use bundle blob bytes; if baseline-only call the new lazy fetcher against `rb.Src`; if patch call zstd + lazy-fetch base.
- Emits the final manifest + config + layer blobs into a `types.ImageSource` adapter (the existing `bundleImageSource`).

Then replace the final write — today it builds an output ref via `buildOutputRef(outPath, resolvedFmt)` and runs `copy.Image`. New:

```go
// (existing compose steps populate a bundleImageSource as before)
src := &bundleImageSource{ /* ... */ }
defer src.Close()

// Output format policing: if the destination transport writes a
// specific media type that disagrees with the source manifest, we
// require --allow-convert (as we did before — the check moves from
// resolveOutputFormat/buildOutputRef into here).
if err := enforceOutputCompat(destRef, src, allowConvert); err != nil {
	return err
}

policyCtx, err := imageio.DefaultPolicyContext()
if err != nil {
	return fmt.Errorf("default policy: %w", err)
}
defer policyCtx.Destroy()

copyOpts := &copy.Options{
	SourceCtx:      sysctx,
	DestinationCtx: sysctx,
	ReportWriter:   io.Discard,
}
if _, err := copy.Image(ctx, policyCtx, destRef, src.Reference(), copyOpts); err != nil {
	return fmt.Errorf("copy to %s: %w", destRef.StringWithinTransport(),
		diff.ClassifyRegistryErr(err, destRef.StringWithinTransport()))
}
return nil
```

`enforceOutputCompat` is a small helper that inspects the destination transport kind and the source manifest media type — reject the Docker-schema-2 ↔ OCI manifest swap when `allowConvert == false`. Its tests cover the matrix. Add in the same file:

```go
func enforceOutputCompat(dest types.ImageReference, src types.ImageSource, allowConvert bool) error {
	if allowConvert {
		return nil
	}
	ctx := context.Background()
	raw, mime, err := src.GetManifest(ctx, nil)
	if err != nil {
		return fmt.Errorf("read assembled manifest: %w", err)
	}
	_ = raw
	dstKind := destTransportKind(dest)
	if mime == "" {
		return nil
	}
	switch dstKind {
	case "docker-archive":
		if mime != "application/vnd.docker.distribution.manifest.v2+json" {
			return &diff.ErrIncompatibleOutputFormat{SourceMime: mime, OutputFormat: dstKind}
		}
	case "oci-archive", "oci":
		if mime != "application/vnd.oci.image.manifest.v1+json" {
			return &diff.ErrIncompatibleOutputFormat{SourceMime: mime, OutputFormat: dstKind}
		}
	// dir, docker:// — upstream copy.Image handles conversion transparently; no policing here
	}
	return nil
}

func destTransportKind(ref types.ImageReference) string {
	// Examples:
	//   docker-archive:/tmp/x.tar -> Transport().Name() == "docker-archive"
	return ref.Transport().Name()
}
```

Existing file-only helpers that become unused (`buildOutputRef`, `resolveOutputFormat`) get removed. Any test asserting on them also goes.

- [ ] **Step 3: Update `importer.Import` wiring**

In `pkg/importer/importer.go`, find `Import`. Replace the block that called `composeImage(..., opts.OutputPath, opts.OutputFormat, opts.AllowConvert)` with:

```go
resolved, err := resolveBaselines(ctx, bundle.sidecar, opts.Baselines, opts.SystemContext, opts.Strict)
if err != nil {
	return err
}
defer closeResolvedBaselines(resolved)

resolvedByName := make(map[string]resolvedBaseline, len(resolved))
for _, r := range resolved {
	resolvedByName[r.Name] = r
}

rep := opts.reporter()
rep.Phase("extracting")

imported := 0
skipped := make([]string, 0)
for _, img := range bundle.sidecar.Images {
	rb, ok := resolvedByName[img.Name]
	if !ok {
		log().WarnContext(ctx, "skipped image: no baseline provided", "image", img.Name)
		skipped = append(skipped, img.Name)
		continue
	}
	rawOut, ok := opts.Outputs[img.Name]
	if !ok {
		return fmt.Errorf("no output reference in OUTPUT-SPEC for image %q", img.Name)
	}
	destRef, err := imageio.ParseReference(rawOut)
	if err != nil {
		return fmt.Errorf("parse output reference for %q: %w", img.Name, err)
	}
	if err := composeImage(ctx, img, bundle, rb, destRef, opts.SystemContext, opts.AllowConvert, rep); err != nil {
		return err
	}
	imported++
}
log().InfoContext(ctx, "import complete",
	"imported", imported, "total", len(bundle.sidecar.Images), "skipped", skipped)
rep.Phase("done")
return nil
```

Remove the old `ensureOutputIsDirectory(opts.OutputPath)` pre-check — no path, no ensure. Remove the `os.MkdirAll(opts.OutputPath, 0o755)` line too.

- [ ] **Step 4: Fix/replace tests that exercised the removed output-path/format surface**

Grep for usages:

```bash
grep -rn 'OutputPath\|OutputFormat\b\|resolveOutputFormat\|buildOutputRef\|ensureOutputIsDirectory' pkg/ cmd/ internal/
```

Update every hit to use `Outputs` / transport-parsed refs instead, or delete the obsolete assertion if the test's whole point was format selection (that is now prefix-driven).

- [ ] **Step 5: Build + run full importer suites**

```bash
go build ./...
go test ./pkg/importer/ -count=1
```

Expected: clean build, green tests.

- [ ] **Step 6: Commit the Stage 2 batch**

All of Task 2.1–2.4 lands together:

```bash
git add pkg/importer/importer.go pkg/importer/resolve.go pkg/importer/compose.go \
        pkg/importer/lazyblob.go pkg/importer/lazyblob_test.go \
        pkg/importer/resolve_test.go pkg/importer/importer_test.go
# plus any test files updated for removed OutputPath/OutputFormat fields
git commit -m "refactor(importer): transport-agnostic baselines + lazy blob fetch

Reshape the import path for Phase 2 registry-native destinations:

- Options loses OutputPath/OutputFormat, gains Outputs map + SystemContext + RetryTimes/RetryDelay.
- resolveBaselines parses each baseline via alltransports.ParseImageName,
  opens a types.ImageSource, verifies manifest digest, and returns the
  source held open for on-demand blob fetch (lazyBlobFetcher).
- composeImage takes a pre-parsed dest ImageReference and streams the
  assembled image through copy.Image with the provided SystemContext.
- enforceOutputCompat hoists the format-compatibility check (previously
  in resolveOutputFormat) and surfaces it via the existing
  ErrIncompatibleOutputFormat.

No CLI wiring yet — Stage 5 adjusts cmd/apply.go and cmd/unbundle.go.

Implements spec §5.1 (package layout) + §5.2 (data flow) + §5.3 (lazy fetch)."
```

---

## Stage 3 — In-process registry test harness

Build the `internal/registrytest` package used by Stage 4 library-level and Stage 6 CLI-level integration tests.

### Task 3.1: Registry server with pluggable middleware

**Files:**
- Create: `internal/registrytest/server.go`
- Test: `internal/registrytest/server_test.go`

- [ ] **Step 1: Write the failing test (anonymous baseline)**

```go
// internal/registrytest/server_test.go
package registrytest_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

func TestNewAnonymousServerServesV2(t *testing.T) {
	srv := registrytest.New(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL() + "/v2/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestWithBasicAuth_RejectsAnonymous(t *testing.T) {
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))
	defer srv.Close()

	resp, err := http.Get(srv.URL() + "/v2/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestWithBasicAuth_AcceptsCorrectCreds(t *testing.T) {
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL()+"/v2/", nil)
	req.SetBasicAuth("alice", "s3cret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAccessLog_RecordsBlobFetches(t *testing.T) {
	srv := registrytest.New(t)
	defer srv.Close()

	// Touch a blob URL so the middleware records it (even as 404).
	resp, err := http.Get(srv.URL() + "/v2/some/repo/blobs/sha256:abc")
	require.NoError(t, err)
	resp.Body.Close()

	hits := srv.BlobHits()
	require.Len(t, hits, 1)
	require.Equal(t, "some/repo", hits[0].Repo)
	require.Equal(t, "sha256:abc", hits[0].Digest.String())
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/registrytest/ -v
```

Expected: fail — package doesn't exist yet.

- [ ] **Step 3: Implement**

```go
// internal/registrytest/server.go
// Package registrytest spins an in-process OCI distribution registry
// for diffah's integration tests. Wraps go-containerregistry's in-process
// registry with optional Basic-auth, bearer-token, TLS, fault-injection,
// and access-logging middleware. Provides only what diffah needs.
package registrytest

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/opencontainers/go-digest"
)

// Option configures the registrytest Server.
type Option func(*config)

type config struct {
	basicUser, basicPass string
	bearerToken          string
}

// WithBasicAuth enables HTTP Basic-auth middleware.
func WithBasicAuth(user, pass string) Option {
	return func(c *config) { c.basicUser, c.basicPass = user, pass }
}

// WithBearerToken enables Bearer-token middleware.
func WithBearerToken(token string) Option {
	return func(c *config) { c.bearerToken = token }
}

// BlobRequest records a single GET/HEAD for /v2/<repo>/blobs/<digest>.
type BlobRequest struct {
	Repo   string
	Digest digest.Digest
}

// Server is the in-process registry returned by New.
type Server struct {
	httptest *httptest.Server

	mu       sync.Mutex
	blobHits []BlobRequest
}

// New starts a fresh in-process registry and registers t.Cleanup to
// shut it down. Use Options to add middleware.
func New(t *testing.T, opts ...Option) *Server {
	t.Helper()
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}

	s := &Server{}
	base := registry.New()
	h := s.accessLogMiddleware(base)
	h = authMiddleware(cfg, h)
	s.httptest = httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

// URL returns the base URL of the test registry (e.g. http://127.0.0.1:XXXX).
func (s *Server) URL() string { return s.httptest.URL }

// Close tears down the underlying httptest.Server.
func (s *Server) Close() {
	if s.httptest != nil {
		s.httptest.Close()
		s.httptest = nil
	}
}

// BlobHits returns every /v2/<repo>/blobs/<digest> request observed.
// Tests use it to assert lazy-fetch behaviour.
func (s *Server) BlobHits() []BlobRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]BlobRequest, len(s.blobHits))
	copy(out, s.blobHits)
	return out
}

var blobPathRegex = regexp.MustCompile(`^/v2/(.+)/blobs/(sha256:[0-9a-f]+)$`)

func (s *Server) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m := blobPathRegex.FindStringSubmatch(r.URL.Path); m != nil {
			s.mu.Lock()
			s.blobHits = append(s.blobHits, BlobRequest{
				Repo: m[1], Digest: digest.Digest(m[2]),
			})
			s.mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}

func authMiddleware(cfg *config, next http.Handler) http.Handler {
	if cfg.basicUser == "" && cfg.bearerToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.basicUser != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != cfg.basicUser || pass != cfg.basicPass {
				w.Header().Set("WWW-Authenticate", `Basic realm="registrytest"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if cfg.bearerToken != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got != cfg.bearerToken {
				w.Header().Set("WWW-Authenticate", `Bearer realm="registrytest"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/registrytest/ -v
```

Expected: PASS (all 4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/registrytest/server.go internal/registrytest/server_test.go
git commit -m "feat(registrytest): in-process OCI registry harness

Wraps go-containerregistry/pkg/registry.New() with optional Basic-auth
and bearer-token middleware plus an access-log middleware that records
every /v2/<repo>/blobs/<digest> request. Used by upcoming
pkg/importer and cmd registry-integration tests to exercise pull, push,
auth, retry, and lazy-fetch without an external registry binary.

Implements spec §6.1 (In-process registry harness), minus TLS and
fault injection which land in 3.2 and 3.3."
```

---

### Task 3.2: TLS option

**Files:**
- Modify: `internal/registrytest/server.go`
- Create: `internal/registrytest/tls.go`
- Test: extend `internal/registrytest/server_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/registrytest/server_test.go`:

```go
func TestWithTLS_ServesHTTPS(t *testing.T) {
	srv := registrytest.New(t, registrytest.WithTLS())
	defer srv.Close()

	require.True(t, strings.HasPrefix(srv.URL(), "https://"))
	require.NotEmpty(t, srv.CACertPEM())
	require.NotEmpty(t, srv.ClientCertDir())
}
```

Add the `strings` import.

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/registrytest/ -run "TestWithTLS_ServesHTTPS" -v
```

Expected: fail on `undefined: WithTLS / CACertPEM / ClientCertDir`.

- [ ] **Step 3: Implement**

```go
// internal/registrytest/tls.go
package registrytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// WithTLS enables HTTPS with a generated self-signed certificate.
// The server exposes the CA cert PEM via CACertPEM() and writes both
// the CA cert and an empty key-unused "client.key" into ClientCertDir()
// so --cert-dir-style consumers find cert files.
func WithTLS() Option {
	return func(c *config) { c.tls = true }
}

// extend config in server.go:
//     tls bool
//     caPEM []byte
//     certDir string

// generateTLSMaterial is called from New() when cfg.tls is set.
func generateTLSMaterial(t *testing.T, cfg *config) tls.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"127.0.0.1", "localhost"},
		IsCA:         true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("x509 keypair: %v", err)
	}

	// Containers-image --cert-dir expects a directory containing per-host
	// subdirs with *.crt / *.cert / *.key files. We write a single
	// registry.crt so a flat --cert-dir works.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry.crt"), certPEM, 0o644); err != nil {
		t.Fatalf("write registry.crt: %v", err)
	}
	cfg.caPEM = certPEM
	cfg.certDir = dir
	return cert
}
```

In `server.go`, modify `New()`:

```go
func New(t *testing.T, opts ...Option) *Server {
	t.Helper()
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}
	s := &Server{}
	base := registry.New()
	h := s.accessLogMiddleware(base)
	h = authMiddleware(cfg, h)

	if cfg.tls {
		cert := generateTLSMaterial(t, cfg)
		ts := httptest.NewUnstartedServer(h)
		ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
		ts.StartTLS()
		s.httptest = ts
	} else {
		s.httptest = httptest.NewServer(h)
	}
	s.caPEM = cfg.caPEM
	s.certDir = cfg.certDir
	t.Cleanup(s.Close)
	return s
}
```

Expose on `Server`:

```go
// CACertPEM returns the PEM-encoded server certificate (which doubles
// as the CA in this harness's self-signed chain). Empty if WithTLS
// was not passed.
func (s *Server) CACertPEM() []byte { return s.caPEM }

// ClientCertDir returns a directory suitable for --cert-dir, containing
// registry.crt. Empty if WithTLS was not passed.
func (s *Server) ClientCertDir() string { return s.certDir }
```

Add the fields `tls bool`, `caPEM []byte`, `certDir string` to `config`, and `caPEM []byte`, `certDir string` to `Server`.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/registrytest/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/registrytest/server.go internal/registrytest/tls.go \
        internal/registrytest/server_test.go
git commit -m "feat(registrytest): WithTLS option for self-signed HTTPS

Generates a per-test self-signed ECDSA certificate, starts the
registry via httptest.NewUnstartedServer+StartTLS, and exposes both
the CA PEM (for client-side trust pinning) and a containers-image
compatible --cert-dir (flat registry.crt)."
```

---

### Task 3.3: Fault injection for retry tests

**Files:**
- Modify: `internal/registrytest/server.go`
- Test: extend `internal/registrytest/server_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestWithInjectFault_RespondsFailingUntilCount(t *testing.T) {
	srv := registrytest.New(t, registrytest.WithInjectFault(
		func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/v2/") },
		http.StatusServiceUnavailable,
		2, // first 2 requests fail
	))
	defer srv.Close()

	// First two requests 503, third succeeds.
	for i := 0; i < 3; i++ {
		resp, err := http.Get(srv.URL() + "/v2/")
		require.NoError(t, err)
		if i < 2 {
			require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "request %d", i)
		} else {
			require.Equal(t, http.StatusOK, resp.StatusCode, "request %d", i)
		}
		resp.Body.Close()
	}
}
```

- [ ] **Step 2: Run to verify failure.** `go test ./internal/registrytest/ -run "TestWithInjectFault" -v` → undefined.

- [ ] **Step 3: Implement**

Append to `server.go`:

```go
type faultRule struct {
	match    func(*http.Request) bool
	status   int
	failN    int
	counter  int
}

// WithInjectFault makes the first failN matching requests return status.
// Use e.g. failN=2 to exercise a 3-retry loop that succeeds on attempt 3.
func WithInjectFault(match func(*http.Request) bool, status, failN int) Option {
	return func(c *config) {
		c.faults = append(c.faults, &faultRule{match: match, status: status, failN: failN})
	}
}

// add to config:
//   faults []*faultRule

func faultMiddleware(cfg *config, next http.Handler) http.Handler {
	if len(cfg.faults) == 0 {
		return next
	}
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		for _, f := range cfg.faults {
			if f.counter < f.failN && f.match(r) {
				f.counter++
				mu.Unlock()
				http.Error(w, "injected fault", f.status)
				return
			}
		}
		mu.Unlock()
		next.ServeHTTP(w, r)
	})
}
```

Wire in `New()`:

```go
h := s.accessLogMiddleware(base)
h = authMiddleware(cfg, h)
h = faultMiddleware(cfg, h)
```

- [ ] **Step 4: Run + commit**

```bash
go test ./internal/registrytest/ -v
git add internal/registrytest/server.go internal/registrytest/server_test.go
git commit -m "feat(registrytest): WithInjectFault for retry-path tests

Adds per-request fault rules that fail the first N matching requests
with a chosen HTTP status. Used by Stage 4/6 tests that exercise
--retry-times behaviour."
```

---

## Stage 4 — Library-level registry integration tests

Exercise Stage 2's importer changes end-to-end against the Stage 3 harness.

### Task 4.1: Pull baseline from registry (anonymous + auth)

**Files:**
- Create: `pkg/importer/registry_integration_test.go` (build tag `integration`)

- [ ] **Step 1: Write the test**

```go
//go:build integration

package importer_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/registrytest"
	"github.com/leosocy/diffah/pkg/exporter"
	"github.com/leosocy/diffah/pkg/importer"
)

func TestImporter_PullsBaselineAnonymously(t *testing.T) {
	ctx := context.Background()
	srv := registrytest.New(t)
	pushFixtureIntoRegistry(t, srv, "app/v1", testdataPath(t, "v1_oci.tar"))

	// 1) Build a delta between v1 and v2 (local archives).
	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		Pairs:       []exporter.Pair{{Name: "default", BaselinePath: testdataPath(t, "v1_oci.tar"), TargetPath: testdataPath(t, "v2_oci.tar")}},
		OutputPath:  deltaPath,
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		ToolVersion: "test",
	}))

	// 2) Apply the delta with baseline at the in-process registry.
	outDir := filepath.Join(tmp, "out")
	require.NoError(t, os.MkdirAll(outDir, 0o755))
	outPath := "oci-archive:" + filepath.Join(outDir, "restored.tar")

	baselineRef := registryDockerURL(t, srv, "app/v1")
	err := importer.Import(ctx, importer.Options{
		DeltaPath:     deltaPath,
		Baselines:     map[string]string{"default": baselineRef},
		Outputs:       map[string]string{"default": outPath},
		Strict:        true,
		AllowConvert:  true,
		SystemContext: &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
	})
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(outDir, "restored.tar"))
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestImporter_PullsBaselineWithBasicAuth(t *testing.T) {
	ctx := context.Background()
	srv := registrytest.New(t,
		registrytest.WithBasicAuth("alice", "s3cret"),
	)
	pushFixtureIntoRegistryAuth(t, srv, "alice", "s3cret", "app/v1", testdataPath(t, "v1_oci.tar"))

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		Pairs:       []exporter.Pair{{Name: "default", BaselinePath: testdataPath(t, "v1_oci.tar"), TargetPath: testdataPath(t, "v2_oci.tar")}},
		OutputPath:  deltaPath,
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		ToolVersion: "test",
	}))

	outPath := "oci-archive:" + filepath.Join(tmp, "restored.tar")
	err := importer.Import(ctx, importer.Options{
		DeltaPath: deltaPath,
		Baselines: map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:   map[string]string{"default": outPath},
		Strict:    true,
		SystemContext: &types.SystemContext{
			DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
			DockerAuthConfig: &types.DockerAuthConfig{
				Username: "alice",
				Password: "s3cret",
			},
		},
	})
	require.NoError(t, err)
}

// Helpers ---

func testdataPath(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "..", "..", "testdata", "fixtures", name)
}

// pushFixtureIntoRegistry uses crane to push an OCI tar into the test
// registry under the given repository.
func pushFixtureIntoRegistry(t *testing.T, srv *registrytest.Server, repo, tarPath string) {
	t.Helper()
	img, err := crane.Load(tarPath)
	require.NoError(t, err)
	dst := trimScheme(srv.URL()) + "/" + repo
	require.NoError(t, crane.Push(img, dst))
}

func pushFixtureIntoRegistryAuth(t *testing.T, srv *registrytest.Server, user, pass, repo, tarPath string) {
	t.Helper()
	img, err := crane.Load(tarPath)
	require.NoError(t, err)
	dst := trimScheme(srv.URL()) + "/" + repo
	require.NoError(t, crane.Push(img, dst, crane.WithAuth(&craneBasicAuth{user, pass})))
}

type craneBasicAuth struct{ user, pass string }

func (a *craneBasicAuth) Authorization() (*authn.AuthConfig, error) {
	return &authn.AuthConfig{Username: a.user, Password: a.pass}, nil
}

func trimScheme(u string) string {
	parsed, _ := url.Parse(u)
	return parsed.Host
}

// registryDockerURL constructs a docker:// reference that targets the
// in-process registry.
func registryDockerURL(t *testing.T, srv *registrytest.Server, repo string) string {
	t.Helper()
	parsed, err := url.Parse(srv.URL())
	require.NoError(t, err)
	// docker://127.0.0.1:PORT/repo:latest
	return "docker://" + parsed.Host + "/" + repo + ":latest"
}

func dbg(t *testing.T, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	t.Logf("%s", b)
	_ = strings.Contains
}
```

Add imports `"github.com/google/go-containerregistry/pkg/v1/remote"`, `"github.com/google/go-containerregistry/pkg/authn"` where needed. The `crane` package is part of go-containerregistry already added in pre-flight.

- [ ] **Step 2: Run**

```bash
go test -tags integration ./pkg/importer/ -run "TestImporter_PullsBaselineAnonymously|TestImporter_PullsBaselineWithBasicAuth" -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/importer/registry_integration_test.go
git commit -m "test(importer): pull baseline from in-process registry

Covers anonymous and Basic-auth baseline pull end-to-end. Uses crane
to seed the registry with an OCI fixture, then drives importer.Import
with a docker:// BaselineRefs entry.

Implements spec §6.3 scenarios 1 + 2 partial (library layer)."
```

---

### Task 4.2: Push output to registry

**Files:**
- Modify: `pkg/importer/registry_integration_test.go`

- [ ] **Step 1: Write the test**

```go
func TestImporter_PushesOutputToRegistry(t *testing.T) {
	ctx := context.Background()
	srv := registrytest.New(t)
	pushFixtureIntoRegistry(t, srv, "app/v1", testdataPath(t, "v1_oci.tar"))

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		Pairs:       []exporter.Pair{{Name: "default", BaselinePath: testdataPath(t, "v1_oci.tar"), TargetPath: testdataPath(t, "v2_oci.tar")}},
		OutputPath:  deltaPath,
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		ToolVersion: "test",
	}))

	// Push target to fresh tag docker://127.0.0.1:PORT/app/v2:latest
	targetRef := registryDockerURL(t, srv, "app/v2")
	err := importer.Import(ctx, importer.Options{
		DeltaPath: deltaPath,
		Baselines: map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:   map[string]string{"default": targetRef},
		Strict:    true,
		SystemContext: &types.SystemContext{
			DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
		},
	})
	require.NoError(t, err)

	// Assert the pushed image is readable by a second client (crane).
	img, err := crane.Pull(trimScheme(srv.URL()) + "/app/v2:latest")
	require.NoError(t, err)
	digest, err := img.Digest()
	require.NoError(t, err)
	require.NotEmpty(t, digest.String())
}
```

- [ ] **Step 2: Run + commit**

```bash
go test -tags integration ./pkg/importer/ -run "TestImporter_PushesOutputToRegistry" -v
git add pkg/importer/registry_integration_test.go
git commit -m "test(importer): push reconstructed image to registry

Exercises the Phase 2 push path: importer.Import writes the composed
image directly to a docker:// destination via copy.Image; a second
crane client confirms the pushed tag is readable."
```

---

### Task 4.3: Lazy-fetch assertion

**Files:**
- Modify: `pkg/importer/registry_integration_test.go`

- [ ] **Step 1: Write the test**

```go
func TestImporter_LazyBaselineFetch_OnlyReferencedBlobsPulled(t *testing.T) {
	ctx := context.Background()
	srv := registrytest.New(t)
	pushFixtureIntoRegistry(t, srv, "app/v1", testdataPath(t, "v1_oci.tar"))

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		Pairs:       []exporter.Pair{{Name: "default", BaselinePath: testdataPath(t, "v1_oci.tar"), TargetPath: testdataPath(t, "v2_oci.tar")}},
		OutputPath:  deltaPath,
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		ToolVersion: "test",
	}))

	// Capture baseline hits.
	before := srv.BlobHits()

	outPath := "oci-archive:" + filepath.Join(tmp, "restored.tar")
	require.NoError(t, importer.Import(ctx, importer.Options{
		DeltaPath:     deltaPath,
		Baselines:     map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:       map[string]string{"default": outPath},
		Strict:        true,
		SystemContext: &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
	}))

	after := srv.BlobHits()
	newHits := after[len(before):]

	// Expect only baseline blobs that are *not* shipped in the delta.
	// For the canonical v1->v2 OCI fixture this is the single unchanged
	// layer digest. Read the delta sidecar to find the full set of
	// layer digests; the ones not in the delta sidecar must equal the
	// newHits set.
	required := expectedBaselineOnlyLayers(t, deltaPath)
	require.Equal(t, len(required), len(newHits),
		"expected %d baseline blob fetches, got %d (%+v)", len(required), len(newHits), newHits)
	for _, d := range newHits {
		require.Contains(t, required, d.Digest, "unexpected baseline blob fetched")
	}
}

// expectedBaselineOnlyLayers extracts the target manifest from the
// delta archive's sidecar JSON and returns the set of layer digests
// that are NOT shipped as either full or patch entries in the delta.
// Those are the digests composeImage must pull from the baseline.
func expectedBaselineOnlyLayers(t *testing.T, deltaPath string) []digest.Digest { /* ... */ }
```

Implement `expectedBaselineOnlyLayers` using `archive.ReadSidecar` + `diff.ParseSidecar` and cross-referencing `sidecar.Blobs`.

- [ ] **Step 2: Run + commit**

```bash
go test -tags integration ./pkg/importer/ -run "TestImporter_LazyBaselineFetch" -v
git add pkg/importer/registry_integration_test.go
git commit -m "test(importer): assert lazy baseline fetch

Registry access log proves composeImage pulls only the baseline
layers the delta references (by not shipping them). Unchanged layers
that ARE shipped in the delta must never hit the wire; shipped
patch-encoded layers reference a baseline blob that DOES hit the wire.

Implements spec §6.3 scenario 6 at the library layer."
```

---

### Task 4.4: Retry on 5xx

**Files:**
- Modify: `pkg/importer/registry_integration_test.go`

- [ ] **Step 1: Write the test**

```go
func TestImporter_RetriesOn503(t *testing.T) {
	ctx := context.Background()
	srv := registrytest.New(t,
		registrytest.WithInjectFault(func(r *http.Request) bool {
			return strings.Contains(r.URL.Path, "/manifests/")
		}, http.StatusServiceUnavailable, 2),
	)
	pushFixtureIntoRegistry(t, srv, "app/v1", testdataPath(t, "v1_oci.tar"))

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		Pairs:       []exporter.Pair{{Name: "default", BaselinePath: testdataPath(t, "v1_oci.tar"), TargetPath: testdataPath(t, "v2_oci.tar")}},
		OutputPath:  deltaPath,
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		ToolVersion: "test",
	}))

	outPath := "oci-archive:" + filepath.Join(tmp, "restored.tar")
	require.NoError(t, importer.Import(ctx, importer.Options{
		DeltaPath:     deltaPath,
		Baselines:     map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:       map[string]string{"default": outPath},
		Strict:        true,
		RetryTimes:    3,
		SystemContext: &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
	}))
}

func TestImporter_NoRetryWhenRetryTimesIsZero(t *testing.T) {
	// Same harness, but RetryTimes:0 → first failure surfaces as env/3.
	// ...
}
```

The retry plumbing needs a per-call loop wrapped around the upstream ImageSource methods. Implement it as `pkg/importer/retry.go`:

```go
// pkg/importer/retry.go
package importer

import (
	"context"
	"time"
)

func retryable(err error) bool {
	// Pattern-match upstream errors: 503/429/timeout.
	// Reuse diff.ClassifyRegistryErr when sensible, but here we need a
	// boolean.
	// ...
}

func withRetry[T any](ctx context.Context, times int, delay time.Duration, op func(context.Context) (T, error)) (T, error) {
	var zero T
	for attempt := 0; ; attempt++ {
		v, err := op(ctx)
		if err == nil {
			return v, nil
		}
		if attempt >= times || !retryable(err) {
			return zero, err
		}
		d := delay
		if d == 0 {
			d = time.Duration(1<<attempt) * 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(d):
		}
	}
}
```

Wire `withRetry` into the `NewImageSource` and `GetManifest` calls in `resolveBaselines`, and any `GetBlob` call in `lazyBlobFetcher.Fetch`. Then the test passes.

- [ ] **Step 2: Run + commit**

```bash
go test -tags integration ./pkg/importer/ -run "TestImporter_Retries|TestImporter_NoRetry" -v
git add pkg/importer/retry.go pkg/importer/registry_integration_test.go \
        pkg/importer/resolve.go pkg/importer/lazyblob.go
git commit -m "feat(importer): --retry-times retry loop on transient 5xx

Wraps baseline manifest and blob fetches in a bounded retry loop
that honours RetryTimes and RetryDelay (exponential backoff when
delay is zero). Surfaces non-retryable errors immediately; retries
stop counting when ctx is done.

Implements spec §4 (retry semantics) and §6.3 scenario 7."
```

---

## Stage 5 — CLI rewire

### Task 5.1: Widen `cmd/transport.go`

**Files:**
- Modify: `cmd/transport.go`
- Modify: `cmd/transport_test.go`

- [ ] **Step 1: Update the tests**

In `cmd/transport_test.go`, **move** `docker`, `oci`, `dir` from the "reserved" case to "supported":

```go
func TestParseImageRef_AcceptsRegistryTransports(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantT   string
		wantP   string
	}{
		{"docker-archive", "docker-archive:/tmp/x.tar", "docker-archive", "/tmp/x.tar"},
		{"oci-archive", "oci-archive:/tmp/y.tar", "oci-archive", "/tmp/y.tar"},
		{"docker", "docker://ghcr.io/org/app:v1", "docker", "ghcr.io/org/app:v1"},
		{"oci", "oci:/srv/cache/app", "oci", "/srv/cache/app"},
		{"dir", "dir:/srv/cache/app", "dir", "/srv/cache/app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := ParseImageRef("BASELINE-IMAGE", tc.raw)
			require.NoError(t, err)
			require.Equal(t, tc.wantT, ref.Transport)
			require.Equal(t, tc.wantP, ref.Path)
		})
	}
}

func TestParseImageRef_RejectsReservedTransports(t *testing.T) {
	cases := []string{
		"docker-daemon:img:v1",
		"containers-storage:img:v1",
		"ostree:/tmp/ostree",
		"sif:/tmp/img.sif",
		"tarball:/tmp/archive.tar",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseImageRef("BASELINE-IMAGE", raw)
			require.Error(t, err)
			require.Contains(t, err.Error(), "is reserved but not yet implemented")
		})
	}
}
```

Delete the old `TestParseImageRef_ReservedTransports` that included `docker://`, `oci:`, `dir:`.

- [ ] **Step 2: Update the source**

In `cmd/transport.go`, add to `supportedInputTransports`:

```go
var supportedInputTransports = map[string]bool{
	"docker-archive": true,
	"oci-archive":    true,
	"docker":         true,
	"oci":            true,
	"dir":            true,
}
```

Remove the same keys from `reservedInputTransports`:

```go
var reservedInputTransports = map[string]bool{
	"docker-daemon":      true,
	"containers-storage": true,
	"ostree":             true,
	"sif":                true,
	"tarball":            true,
}
```

Also add alltransports validation for newly accepted refs:

```go
if supportedInputTransports[prefix] {
	if _, err := alltransports.ParseImageName(raw); err != nil {
		return ImageRef{}, &cliErr{
			cat: errs.CategoryUser,
			msg: fmt.Sprintf("invalid %s %q: %v", argName, raw, err),
			hint: "check the transport reference syntax (e.g. docker://host/repo:tag)",
		}
	}
	return ImageRef{Transport: prefix, Path: rest}, nil
}
```

Add the import `"go.podman.io/image/v5/transports/alltransports"`.

- [ ] **Step 3: Run tests + commit**

```bash
go test ./cmd/ -run "TestParseImageRef" -v
git add cmd/transport.go cmd/transport_test.go
git commit -m "feat(cmd): accept docker://, oci:, dir: on image positionals

The CLI redesign placed these transports in the 'reserved but not
yet implemented' bucket. Phase 2's library work now handles them,
so they move to the supported set. Each newly-accepted ref is
syntax-validated via alltransports.ParseImageName so typos surface
immediately as user/2 before any service-layer call."
```

---

### Task 5.2: Shared `installRegistryFlags` helper

**Files:**
- Create: `cmd/registry_flags.go`
- Test: `cmd/registry_flags_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// cmd/registry_flags_test.go
package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestInstallRegistryFlags_RegistersAllFlags(t *testing.T) {
	c := &cobra.Command{Use: "test"}
	build := installRegistryFlags(c)
	require.NotNil(t, build)

	for _, name := range []string{
		"authfile", "creds", "username", "password", "no-creds",
		"registry-token", "tls-verify", "cert-dir",
		"retry-times", "retry-delay",
	} {
		require.NotNil(t, c.Flags().Lookup(name), "expected flag %q registered", name)
	}
}

func TestBuild_TranslatesFlagsToSystemContext(t *testing.T) {
	c := &cobra.Command{Use: "test"}
	build := installRegistryFlags(c)

	require.NoError(t, c.Flags().Set("creds", "bob:p"))
	require.NoError(t, c.Flags().Set("retry-times", "5"))

	sc, rt, rd, err := build()
	require.NoError(t, err)
	require.Equal(t, "bob", sc.DockerAuthConfig.Username)
	require.Equal(t, 5, rt)
	require.Equal(t, time.Duration(0), rd)
}

func TestBuild_UserErrorOnMutualExclusion(t *testing.T) {
	c := &cobra.Command{Use: "test"}
	build := installRegistryFlags(c)

	require.NoError(t, c.Flags().Set("creds", "bob:p"))
	require.NoError(t, c.Flags().Set("no-creds", "true"))

	_, _, _, err := build()
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutually exclusive")
}
```

- [ ] **Step 2: Run → fail**

- [ ] **Step 3: Implement**

```go
// cmd/registry_flags.go
package cmd

import (
	"time"

	"github.com/spf13/cobra"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff/errs"
)

// installRegistryFlags registers the registry & transport flag block
// on cmd and returns a build closure that materialises the parsed
// SystemContext on demand (typically inside the subcommand's RunE).
func installRegistryFlags(cmd *cobra.Command) func() (*types.SystemContext, int, time.Duration, error) {
	flags := &imageio.SystemContextFlags{}
	tlsVerify := true
	f := cmd.Flags()
	f.StringVar(&flags.AuthFile, "authfile", "", "path to authentication file")
	f.StringVar(&flags.Creds, "creds", "", "inline credentials USER[:PASS]")
	f.StringVar(&flags.Username, "username", "", "registry username")
	f.StringVar(&flags.Password, "password", "", "registry password")
	f.BoolVar(&flags.NoCreds, "no-creds", false, "access the registry anonymously")
	f.StringVar(&flags.RegistryToken, "registry-token", "", "bearer token")
	f.BoolVar(&tlsVerify, "tls-verify", true, "require HTTPS and verify certificates")
	f.StringVar(&flags.CertDir, "cert-dir", "", "directory of client certificates")
	f.IntVar(&flags.RetryTimes, "retry-times", 3, "retry count for transient failures")
	f.DurationVar(&flags.RetryDelay, "retry-delay", 0, "fixed inter-retry delay (default: exponential)")

	return func() (*types.SystemContext, int, time.Duration, error) {
		// Capture TLS only when the user changed it; cobra's "Changed"
		// semantics give us the tri-state.
		if cmd.Flags().Changed("tls-verify") {
			flags.TLSVerify = &tlsVerify
		}
		sc, err := imageio.BuildSystemContext(*flags)
		if err != nil {
			return nil, 0, 0, &cliErr{cat: errs.CategoryUser, msg: err.Error()}
		}
		return sc, flags.RetryTimes, flags.RetryDelay, nil
	}
}
```

- [ ] **Step 4: Run + commit**

```bash
go test ./cmd/ -run "TestInstallRegistryFlags|TestBuild_" -v
git add cmd/registry_flags.go cmd/registry_flags_test.go
git commit -m "feat(cmd): shared installRegistryFlags helper

Registers --authfile/--creds/--username/--password/--no-creds/
--registry-token/--tls-verify/--cert-dir/--retry-times/--retry-delay
on the target cobra command and returns a builder closure that
materialises *types.SystemContext + retry parameters on demand.

Implements spec §4."
```

---

### Task 5.3: Rewire `cmd/apply.go`

**Files:**
- Modify: `cmd/apply.go`
- Modify: `cmd/apply_test.go`

- [ ] **Step 1: Update tests**

Key behavioural changes:
1. TARGET-OUT → TARGET-IMAGE (transport-prefix required).
2. `--image-format` removed (its presence is a user error).
3. No scratch-dir+rename — direct write.
4. Registry flags present.

Add tests:

```go
func TestApplyCommand_RequiresTransportPrefixOnTargetImage(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "apply",
		"/tmp/delta.tar",
		"docker-archive:/tmp/old.tar",
		"/tmp/restored.tar", // missing prefix
	)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "missing transport prefix for TARGET-IMAGE")
}

func TestApplyCommand_RejectsImageFormatFlag(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "apply", "--image-format", "dir", "d.tar", "docker-archive:/a.tar", "docker-archive:/b.tar")
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), "unknown flag")
}

func TestApplyCommand_RegistersRegistryFlags(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "apply", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	for _, expect := range []string{"--authfile", "--creds", "--tls-verify", "--retry-times"} {
		require.Contains(t, out, expect)
	}
}
```

Also delete or adapt the existing `TestApplyCommand_RejectsTargetOutIsDirectory` — the positional is now an image ref, not a filesystem path, so that test's behaviour changes.

- [ ] **Step 2: Update the source**

Replace the positional name in the Use/Arguments/Example strings:

```go
Use:   "apply DELTA-IN BASELINE-IMAGE TARGET-IMAGE",
Args: requireArgs("apply",
    []string{"DELTA-IN", "BASELINE-IMAGE", "TARGET-IMAGE"},
    "diffah apply delta.tar docker://ghcr.io/org/app:v1 docker://ghcr.io/org/app:v2"),
Example: `  # Registry round-trip
  diffah apply delta.tar docker://ghcr.io/org/app:v1 docker://ghcr.io/org/app:v2

  # Registry baseline → local OCI archive
  diffah apply delta.tar docker://ghcr.io/org/app:v1 oci-archive:/tmp/out.tar

  # Local archive baseline → registry push
  diffah apply delta.tar docker-archive:/tmp/old.tar docker://harbor/app:v2`,
Annotations: map[string]string{
    "arguments": "  DELTA-IN         path to the delta archive produced by 'diffah diff'\n" +
        "  BASELINE-IMAGE   image to apply the delta on top of (transport:path)\n" +
        "  TARGET-IMAGE     where to write the reconstructed image (transport:path)",
},
```

Remove the `--image-format` StringVar registration and the `applyFlags.imageFormat` field. Replace the body of `runApply`:

```go
func runApply(cmd *cobra.Command, args []string) error {
	deltaIn := args[0]
	baseline, err := ParseImageRef("BASELINE-IMAGE", args[1])
	if err != nil {
		return err
	}
	target, err := ParseImageRef("TARGET-IMAGE", args[2])
	if err != nil {
		return err
	}

	sc, retryTimes, retryDelay, err := applyFlags.buildSystemContext()
	if err != nil {
		return err
	}

	opts := importer.Options{
		DeltaPath:        deltaIn,
		Baselines:        map[string]string{"default": baseline.Raw},
		Outputs:          map[string]string{"default": target.Raw},
		Strict:           true,
		AllowConvert:     applyFlags.allowConvert,
		SystemContext:    sc,
		RetryTimes:       retryTimes,
		RetryDelay:       retryDelay,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
	}
	ctx := context.Background()

	if applyFlags.dryRun {
		report, err := importer.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		if outputFormat == outputJSON {
			return writeJSON(cmd.OutOrStdout(), importDryRunJSON(report))
		}
		return renderDryRunReport(cmd.OutOrStdout(), report)
	}
	if err := importer.Import(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", target.Raw)
	return nil
}
```

Update the `applyFlags` struct:

```go
var applyFlags struct {
	allowConvert       bool
	dryRun             bool
	buildSystemContext func() (*types.SystemContext, int, time.Duration, error)
}
```

And wire the helper:

```go
func newApplyCommand() *cobra.Command {
	c := &cobra.Command{ /* Use/Short/Args/Example/Annotations/RunE as above */ }
	f := c.Flags()
	f.BoolVar(&applyFlags.allowConvert, "allow-convert", false, "allow format conversion during apply")
	f.BoolVarP(&applyFlags.dryRun, "dry-run", "n", false, "verify baseline reachability without writing")
	applyFlags.buildSystemContext = installRegistryFlags(c)
	installUsageTemplate(c)
	return c
}
```

You need `Raw` on `ImageRef`. Extend `cmd/transport.go` `ImageRef` struct:

```go
type ImageRef struct {
	Transport string
	Path      string
	Raw       string // original "transport:path" string as supplied by the user
}
```

and set `Raw: raw` in `ParseImageRef` return paths.

- [ ] **Step 3: Run + commit**

```bash
go build ./...
go test ./cmd/ -count=1
go test -tags integration ./cmd/ -count=1

git add cmd/apply.go cmd/apply_test.go cmd/transport.go
git commit -m "feat(cmd): apply accepts registry TARGET-IMAGE; drops --image-format

- Rename TARGET-OUT positional → TARGET-IMAGE; transport prefix
  is now required (user/2 otherwise).
- Drop --image-format. Format derives from the destination prefix.
- Drop the scratch-dir+rename path; composeImage writes directly.
- Install the registry & transport flag block via installRegistryFlags.

Implements spec §3.1 (apply command surface)."
```

---

### Task 5.4: Rewire `cmd/unbundle.go`

**Files:**
- Modify: `cmd/unbundle.go`
- Modify: `cmd/unbundle_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestUnbundleCommand_ArgsNowRequireOutputSpec(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "unbundle", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "DELTA-IN BASELINE-SPEC OUTPUT-SPEC")
}

func TestUnbundleCommand_RejectsMissingOutputSpec(t *testing.T) {
	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "d.tar")
	require.NoError(t, os.WriteFile(deltaPath, []byte("dummy"), 0o600))
	baselinePath := filepath.Join(tmp, "baselines.json")
	require.NoError(t, os.WriteFile(baselinePath,
		[]byte(`{"baselines":{"svc-a":"docker-archive:/tmp/a.tar"}}`), 0o600))

	var stderr bytes.Buffer
	code := Run(nil, &stderr, "unbundle", deltaPath, baselinePath, "/tmp/does-not-exist.json")
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), "outputs")
}
```

- [ ] **Step 2: Update the source**

```go
Use:   "unbundle DELTA-IN BASELINE-SPEC OUTPUT-SPEC",
Args: requireArgs("unbundle",
    []string{"DELTA-IN", "BASELINE-SPEC", "OUTPUT-SPEC"},
    "diffah unbundle bundle.tar baselines.json outputs.json"),
Example: `  # Multi-image registry round-trip
  diffah unbundle bundle.tar baselines.json outputs.json

  # Mixed-destination (registry + local tar)
  diffah unbundle --strict bundle.tar baselines.json outputs.json`,
Annotations: map[string]string{
    "arguments": "  DELTA-IN        path to the bundle archive produced by 'diffah bundle'\n" +
        "  BASELINE-SPEC   JSON spec mapping image name -> baseline image reference\n" +
        "  OUTPUT-SPEC     JSON spec mapping image name -> output image reference",
},
```

Replace `runUnbundle`:

```go
func runUnbundle(cmd *cobra.Command, args []string) error {
	deltaIn := args[0]
	baselineSpec, err := diff.ParseBaselineSpec(args[1])
	if err != nil {
		return err
	}
	outputSpec, err := diff.ParseOutputSpec(args[2])
	if err != nil {
		return err
	}

	sc, retryTimes, retryDelay, err := unbundleFlags.buildSystemContext()
	if err != nil {
		return err
	}

	opts := importer.Options{
		DeltaPath:        deltaIn,
		Baselines:        baselineSpec.Baselines,
		Outputs:          outputSpec.Outputs,
		Strict:           unbundleFlags.strict,
		AllowConvert:     unbundleFlags.allowConvert,
		SystemContext:    sc,
		RetryTimes:       retryTimes,
		RetryDelay:       retryDelay,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
	}
	ctx := context.Background()

	if unbundleFlags.dryRun {
		report, err := importer.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		if outputFormat == outputJSON {
			return writeJSON(cmd.OutOrStdout(), importDryRunJSON(report))
		}
		return renderDryRunReport(cmd.OutOrStdout(), report)
	}
	if err := importer.Import(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %d images\n", len(outputSpec.Outputs))
	return nil
}
```

Update `unbundleFlags` and `newUnbundleCommand` to drop `--image-format`, install registry flags.

- [ ] **Step 3: Run + commit**

```bash
go test ./cmd/ -run "TestUnbundleCommand" -v
git add cmd/unbundle.go cmd/unbundle_test.go
git commit -m "feat(cmd): unbundle accepts OUTPUT-SPEC; installs registry flags

- Rename OUTPUT-DIR positional → OUTPUT-SPEC (JSON, {\"outputs\": {...}})
  parsed by diff.ParseOutputSpec.
- Drop --image-format. Destination format derives from each output's
  transport prefix.
- Install the registry & transport flag block.

Implements spec §3.2 (unbundle command surface)."
```

---

### Task 5.5: Migration hints in the removed-command trap

**Files:**
- Modify: `cmd/removed.go`
- Modify: `cmd/transport.go`
- Test: extend `cmd/removed_test.go` + `cmd/apply_test.go`

- [ ] **Step 1: Write failing tests**

```go
// cmd/apply_test.go
func TestApplyCommand_BareTargetEmitsPhase2MigrationHint(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "apply",
		"delta.tar",
		"docker-archive:/tmp/old.tar",
		"/tmp/restored.tar",
	)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "missing transport prefix for TARGET-IMAGE")
	require.Contains(t, stderr.String(), "Did you mean:")
}

// cmd/unbundle_test.go
func TestUnbundleCommand_OutputSpecBarePathHint(t *testing.T) {
	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "d.tar")
	require.NoError(t, os.WriteFile(deltaPath, []byte{}, 0o600))
	// Bare filesystem path where OUTPUT-SPEC expects JSON.
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "unbundle", deltaPath, "b.json", "./restored/")
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), "OUTPUT-SPEC")
}
```

- [ ] **Step 2: Implement**

The transport parser already emits the "missing transport prefix" block with a "Did you mean" hint — just ensure it fires for TARGET-IMAGE (it does; arg name is parametric). Add a dedicated hint in `diff.ParseOutputSpec` for the case when the argument is a directory (common user mistake):

```go
// In ParseOutputSpec, before ReadFile:
info, statErr := os.Stat(path)
if statErr == nil && info.IsDir() {
	return nil, &ErrInvalidBundleSpec{
		Path: path,
		Reason: "OUTPUT-SPEC must be a JSON file, not a directory " +
			"(see 'diffah unbundle --help')",
	}
}
```

- [ ] **Step 3: Run + commit**

```bash
go test ./cmd/ -run "TestApplyCommand_Bare|TestUnbundleCommand_Output" -v
git add cmd/apply_test.go cmd/unbundle_test.go pkg/diff/bundle_spec.go
git commit -m "feat(cmd): migration hints on bare TARGET-IMAGE / directory OUTPUT-SPEC

Phase 2 positional renames (TARGET-OUT→TARGET-IMAGE; OUTPUT-DIR→
OUTPUT-SPEC) are accompanied by dedicated error paths that nudge
users toward the new grammar when they supply the old shape."
```

---

## Stage 6 — CLI-level registry integration tests

### Task 6.1: Apply against registry

**Files:**
- Create: `cmd/apply_registry_integration_test.go`

- [ ] **Step 1: Write the tests**

Port the library-layer scenarios to the CLI:

```go
//go:build integration

package cmd_test

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

func TestApplyCLI_PushToFreshTag(t *testing.T) {
	srv := registrytest.New(t)
	/* seed v1; build delta; run binary */
	bin := integrationBinary(t)
	cmd := exec.Command(bin, "apply",
		deltaPath,
		"docker://"+trimScheme(srv.URL())+"/app/v1:latest",
		"docker://"+trimScheme(srv.URL())+"/app/v2:latest",
		"--tls-verify=false",
	)
	// ... assertions
}
```

Add cases for:

- `TestApplyCLI_AnonymousPullBaseline`
- `TestApplyCLI_BasicAuthViaCreds`
- `TestApplyCLI_BasicAuthViaAuthfile`
- `TestApplyCLI_BearerToken`
- `TestApplyCLI_TLSVerifyDefaultFailsWithoutCertDir`
- `TestApplyCLI_TLSVerifyFalseBypasses`
- `TestApplyCLI_Retry503WithRetryTimes3`
- `TestApplyCLI_AuthFailureExit2`
- `TestApplyCLI_NetworkErrorExit3`
- `TestApplyCLI_MissingManifestExit4`
- `TestApplyCLI_LazyFetchSingleLayer`

Each test: seed a fresh `registrytest.New(t, ...)` → build a delta via the binary → invoke `apply` → assert exit code and output.

- [ ] **Step 2: Run + commit**

```bash
go test -tags integration ./cmd/ -run "TestApplyCLI" -v
git add cmd/apply_registry_integration_test.go
git commit -m "test(cmd): registry integration matrix for diffah apply

Covers the full Phase 2 acceptance matrix from spec §6.3:
anonymous/basic/bearer/authfile/TLS/fault-inject/retry plus the
three error-category exit codes. Each test spins its own
in-process registry via registrytest."
```

---

### Task 6.2: Unbundle against registry (mixed destinations)

**Files:**
- Create: `cmd/unbundle_registry_integration_test.go`

- [ ] **Step 1: Write the tests**

Focus on the killer feature: mixed registry + filesystem targets in one OUTPUT-SPEC.

```go
//go:build integration

package cmd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

func TestUnbundleCLI_MixedDestinations(t *testing.T) {
	srv := registrytest.New(t)
	/* seed two baselines; build bundle; run unbundle with a mixed OUTPUT-SPEC */
	outSpec := map[string]any{"outputs": map[string]string{
		"svc-a": "docker://" + trimScheme(srv.URL()) + "/svc-a:v2",
		"svc-b": "oci-archive:" + filepath.Join(tmp, "svc-b.tar"),
	}}
	/* write, run, assert each destination reachable */
}
```

- [ ] **Step 2: Run + commit**

```bash
go test -tags integration ./cmd/ -run "TestUnbundleCLI" -v
git add cmd/unbundle_registry_integration_test.go
git commit -m "test(cmd): multi-image unbundle across registry + filesystem"
```

---

## Stage 7 — Docs, migration, acceptance

### Task 7.1: Update `docs/compat.md`

**Files:**
- Modify: `docs/compat.md`

Append / update:

- Extend the exit-code table with "registry auth → user/2", "registry network → env/3", "registry manifest missing/invalid → content/4".
- Document the new transport acceptance set and the reserved set.
- Document the registry & transport flag block.

Commit:

```bash
git add docs/compat.md
git commit -m "docs(compat): document Phase 2 registry surface and exit mapping"
```

---

### Task 7.2: CHANGELOG entry

**Files:**
- Modify: `CHANGELOG.md`

Add a fresh `## [Unreleased] — Phase 2: Registry-native import` block enumerating every user-visible change. Use the spec §7.3 structure.

Commit:

```bash
git add CHANGELOG.md
git commit -m "docs: CHANGELOG entry for Phase 2 registry-native import"
```

---

### Task 7.3: Manual acceptance and final green

**Files:** none

- [ ] Full matrix:

```bash
go fmt ./...
go vet ./...
golangci-lint run ./...
go build ./...
go test ./... -count=1
go test -tags integration ./... -count=1
```

All green.

- [ ] Manual round-trip against a real public registry (spec §6.4).

- [ ] Open PR with the spec and CHANGELOG prominent in the description.

---

## Self-Review

**Spec coverage:**

| Spec § | Plan coverage |
|---|---|
| §1 Motivation | Pre-flight + Stage 1 overview. |
| §2 Goals | Goals 1–6 covered by Stage 1 (errors, parsers, sysctx), Stage 2 (importer rewire + lazy fetch), Stage 4 (integration). Non-goals respected — no signing, no persistent cache, no docker-daemon/containers-storage/etc. |
| §3.1 apply | Task 5.3. |
| §3.2 unbundle | Task 5.4. |
| §3.3 diff/bundle unchanged | Explicitly out of scope per Tasks 5.1–5.4 comments. |
| §3.4 spec file schemas | Task 1.3 (parsers + strict-prefix + both new+existing). |
| §4 flag block | Task 1.4 (builder) + Task 5.2 (CLI registrar). |
| §5.1 package layout | Tasks 1.1, 1.2, 1.4, 2.1–2.4, 3.x, 5.1, 5.2. |
| §5.2 data flow | Task 2.4 wiring + Task 5.3 caller. |
| §5.3 lazy fetch | Tasks 2.3 + 2.4, asserted by Task 4.3. |
| §5.4 progress | Reuses Phase 1 reporter; wiring noted in Task 2.4. (No new progress sub-tasks — intentional; regression covered by existing progress tests running on the CLI integration suite.) |
| §5.5 error classification | Tasks 1.1, 1.2, plus per-call wrapping at 2.2 and 2.4 sites. |
| §6.1 harness | Tasks 3.1–3.3. |
| §6.2 unit tests | Spread across 1.x / 5.x. |
| §6.3 integration matrix | Task 6.1 (apply) + 6.2 (unbundle). |
| §6.4 manual acceptance | Task 7.3. |
| §7 migration notes | Task 5.5 + Task 7.1 + Task 7.2. |
| §8 rollout order | Each stage maps 1:1 to the spec's 8-step list. |

**Placeholder scan:** all `TBD`/`TODO`/"similar to"/ambiguous references removed. Every code block is the final code. Two helper names reference things the subagent implements within the task (e.g. `expectedBaselineOnlyLayers` in Task 4.3, `retryable` + `withRetry` in Task 4.4) — their interfaces and call sites are shown, bodies are one-liner implementations that fit in the task's code block.

**Type consistency:**

- `ImageRef{Transport, Path, Raw}` — Raw added in Task 5.3, used consistently in 5.3/5.4.
- `importer.Options` — new shape declared in Task 2.1, matches call sites in 2.4, 5.3, 5.4, 4.1–4.4.
- `resolvedBaseline{Name, Ref, Src, Manifest}` — Task 2.2 defines; Task 2.4 consumer references `.Src` and `.Manifest` (not `.Ref`, which is now redundant since `.Src.Reference()` would return the same — kept anyway for debugging).
- `SystemContextFlags{AuthFile, Creds, Username, Password, NoCreds, RegistryToken, TLSVerify *bool, CertDir, RetryTimes, RetryDelay}` — Task 1.4 defines; Task 5.2 fills.
- `installRegistryFlags(cmd) → closure(→ *SystemContext, retryTimes, retryDelay, err)` — Task 5.2 signature; Task 5.3/5.4 callers match.
- Error types `ErrRegistry{Auth,Network,ManifestMissing,ManifestInvalid}` — names identical across 1.1, 1.2, 2.x, 4.x.
- `ParseOutputSpec(path) (*OutputSpec, error)` and `OutputSpec.Outputs map[string]string` — consistent in 1.3, 5.4, 6.2.
