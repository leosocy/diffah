# Phase 3 — Registry-Native Export + Signing — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `diff` / `bundle` symmetric with `apply` / `unbundle` on the registry surface, and add cosign-compatible keyed signing + verification across the four verbs. The delta archive becomes a signed-and-portable artifact that consumers can verify without the internet.

**Architecture:** The export verbs acquire the same 10-flag `installRegistryFlags` block and `*types.SystemContext` plumbing that the import verbs got in Phase 2; `exporter.Pair` fields are renamed (`BaselinePath`→`BaselineRef`, `TargetPath`→`TargetRef`) and `planPair` switches from `imageio.OpenArchiveRef` to `alltransports.ParseImageName`. A new `pkg/signer` package (isolated from exporter/importer dep-graphs) produces and verifies base64-encoded DER ECDSA-P256 signatures over `sha256(jcs(sidecar.json))`, written as `OUT.sig` alongside the archive and optionally `OUT.rekor.json` when `--rekor-url` is set. Verification is fully opt-in via `--verify PATH`.

**Tech Stack:** Go 1.25+, `go.podman.io/image/v5/transports/alltransports`, `crypto/ecdsa` + `crypto/x509` (stdlib), `golang.org/x/crypto/nacl/secretbox`, `golang.org/x/crypto/scrypt`, `sigstore/sigstore@v1.9.5` (already indirect dep; pulled directly at the module line in Phase 6), internal test harness `internal/registrytest` (Phase 2).

**Spec:** `docs/superpowers/specs/2026-04-24-phase3-registry-native-export-signing-design.md`

---

## Preflight — branch & toolchain sanity

- [ ] **Step P.1: Confirm we're on the spec branch**

Run: `git -C /Users/leosocy/workspace/repos/myself/diffah branch --show-current`
Expected: `spec/v2-phase3-registry-native-export-signing`

If output differs, run `git checkout spec/v2-phase3-registry-native-export-signing`.

- [ ] **Step P.2: Confirm the tree is clean**

Run: `git -C /Users/leosocy/workspace/repos/myself/diffah status --short`
Expected: empty (or at most the `.DS_Store` line).

- [ ] **Step P.3: Confirm tests pass on the spec branch baseline**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./...`
Expected: all pass — this is the "green baseline" we'll protect through every phase.

---

## File Structure (decomposition lock-in)

### Existing files modified

| Path | Modification |
|---|---|
| `pkg/exporter/pair.go` | Rename `BaselinePath`→`BaselineRef`, `TargetPath`→`TargetRef` |
| `pkg/exporter/pair_test.go` | Test update for rename |
| `pkg/exporter/exporter.go` | Add `SystemContext`, `RetryTimes`, `RetryDelay`, `SignKeyPath`, `SignKeyPassphrase`, `RekorURL`; sign hook after `Export` |
| `pkg/exporter/perpair.go` | `imageio.OpenArchiveRef` → `alltransports.ParseImageName`; thread `sys` |
| `pkg/exporter/perpair_test.go` | Test updates for new signatures |
| `pkg/exporter/baseline.go` | Thread `sys` through `NewImageSource` call in `readManifestBundle` |
| `pkg/exporter/writer.go` / `assemble.go` | SourceHint derivation: ref-aware helper (not `filepath.Base`) |
| `pkg/exporter/fingerprint.go` | Streaming refactor — `io.TeeReader` + in-place `tar.Reader` |
| `pkg/exporter/exporter_test.go` + `assemble_test.go` + `encode_test.go` + `pool_test.go` + `writer_test.go` | Propagate `BaselineRef`/`TargetRef` rename to fixtures |
| `pkg/diff/bundle_spec.go` | Reject bare-path values in `ParseBundleSpec`; transport-aware resolve |
| `pkg/diff/bundle_spec_test.go` | Bare-path migration-error test |
| `pkg/diff/errors.go` | Add `ErrBundleSpecMissingTransport{FieldPath}` |
| `pkg/importer/importer.go` | Add `VerifyPubKeyPath`, `VerifyRekorURL`; call verify before blob work |
| `cmd/diff.go` | `installRegistryFlags` + `installSigningFlags`; thread into `exporter.Options` |
| `cmd/bundle.go` | Same as `diff.go` |
| `cmd/apply.go` | `installVerifyFlags`; populate `importer.Options.Verify*` |
| `cmd/unbundle.go` | Same as `apply.go` |
| `cmd/diff_test.go` / `cmd/bundle_test.go` | Dry-run signing probe tests |
| `CHANGELOG.md` | `[Unreleased] — Phase 3` section |
| `docs/compat.md` | `Signatures` section |
| `docs/performance.md` | New file or section on baseline-layer bandwidth |

### New files

| Path | Purpose |
|---|---|
| `cmd/sign_flags.go` | `installSigningFlags(*cobra.Command)` helper for `diff` and `bundle` |
| `cmd/verify_flags.go` | `installVerifyFlags(*cobra.Command)` helper for `apply` and `unbundle` |
| `cmd/diff_registry_integration_test.go` | Registry matrix on `diff` |
| `cmd/bundle_registry_integration_test.go` | Registry matrix on `bundle` |
| `cmd/sign_integration_test.go` | Signing round-trip + tamper cases |
| `cmd/verify_integration_test.go` | Verify matrix (signed/unsigned × with/without `--verify`) |
| `pkg/signer/signer.go` | `Sign(ctx, SignRequest) (*Signature, error)` |
| `pkg/signer/verifier.go` | `Verify(ctx, pubKeyPath, payload, *Signature, rekorURL) error` |
| `pkg/signer/cosign.go` | Sidecar file I/O: `WriteSidecars`, `LoadSidecars` |
| `pkg/signer/canonical.go` | `JCSCanonical(v any) ([]byte, error)` — RFC 8785 JCS |
| `pkg/signer/rekor.go` | `UploadEntry(ctx, rekorURL, sig) (*RekorBundle, error)`; no-op when URL empty |
| `pkg/signer/errors.go` | Typed errors: `ErrKeyPassphraseIncorrect`, `ErrKeyEncrypted`, `ErrKeyUnsupportedKDF`, `ErrSignatureInvalid`, `ErrArchiveUnsigned` — all `errs.Categorized` |
| `pkg/signer/signer_test.go` | Sign→verify round-trip unit tests |
| `pkg/signer/canonical_test.go` | JCS property tests |
| `pkg/signer/cosign_compat_test.go` | Gated by `DIFFAH_SIGN_COMPAT=1`; shells out to `cosign verify-blob` |
| `pkg/signer/testdata/` | Committed keypair fixtures (`.key`, `.pub`, one encrypted variant) |
| `pkg/exporter/bandwidth_test.go` | Registry-bandwidth regression gate |

---

# Phase 1 — `exporter.Pair` field rename

Internal rename only — no user-visible change. This lands first because PR 2+ all touch these field names.

### Task 1.1: Update the `Pair` struct and its unit test

**Files:**
- Modify: `pkg/exporter/pair.go`
- Modify: `pkg/exporter/pair_test.go`

- [ ] **Step 1.1.1: Edit `pkg/exporter/pair.go` to rename fields**

```go
package exporter

import (
	"github.com/leosocy/diffah/pkg/diff"
)

type Pair struct {
	Name        string
	BaselineRef string // transport-prefixed reference (e.g. "docker-archive:/tmp/old.tar", "docker://ghcr.io/org/app:v1")
	TargetRef   string
}

func ValidatePairs(pairs []Pair) error {
	if len(pairs) == 0 {
		return &diff.ErrInvalidBundleSpec{Reason: "pairs must be non-empty"}
	}
	seen := make(map[string]struct{}, len(pairs))
	for _, p := range pairs {
		if _, dup := seen[p.Name]; dup {
			return &diff.ErrDuplicateBundleName{Name: p.Name}
		}
		seen[p.Name] = struct{}{}
	}
	return nil
}
```

- [ ] **Step 1.1.2: Edit `pkg/exporter/pair_test.go` to use the new field names**

In every `Pair{...}` struct literal, replace `BaselinePath:` with `BaselineRef:` and `TargetPath:` with `TargetRef:`.

- [ ] **Step 1.1.3: Run the package tests — expect compile errors from other tests that still reference the old names**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/...`
Expected: compile errors referencing `BaselinePath` / `TargetPath` in `assemble_test.go`, `encode_test.go`, `perpair_test.go`, `pool_test.go`, `writer_test.go`, and (once we touch prod code) `assemble.go` / `perpair.go`.

### Task 1.2: Propagate rename through production code

**Files:**
- Modify: `pkg/exporter/perpair.go`
- Modify: `pkg/exporter/assemble.go`
- Modify: any other `pkg/exporter/*.go` that references `p.BaselinePath` / `p.TargetPath`

- [ ] **Step 1.2.1: Find all production-code references to the old field names**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && grep -rn "BaselinePath\|TargetPath" pkg/exporter --include='*.go' | grep -v _test.go`

Expected output lists `perpair.go` (6 hits) and `assemble.go` (1 hit: `filepath.Base(p.BaselinePath)`).

- [ ] **Step 1.2.2: Edit `pkg/exporter/perpair.go` — rename all `p.BaselinePath` / `p.TargetPath` occurrences**

```go
// Before: imageio.OpenArchiveRef(p.BaselinePath)
// After:  imageio.OpenArchiveRef(p.BaselineRef)
// Same for TargetPath → TargetRef. Leave the imageio.OpenArchiveRef call itself in place for now — Phase 3 swaps it out.
// Inside the returned pairPlan struct, rename BaselinePath → BaselineRef.
```

Also update `pairPlan.BaselinePath` → `pairPlan.BaselineRef` in the struct definition.

- [ ] **Step 1.2.3: Edit `pkg/exporter/assemble.go` — update the `SourceHint` derivation**

Change `SourceHint: filepath.Base(p.BaselinePath)` to `SourceHint: filepath.Base(p.BaselineRef)`.

(This still does the right thing for archive-only values — `filepath.Base("docker-archive:/tmp/old.tar")` gives `"old.tar"`. Phase 3 fixes the `docker://` case.)

### Task 1.3: Propagate rename through remaining test files

**Files:**
- Modify: `pkg/exporter/assemble_test.go`
- Modify: `pkg/exporter/encode_test.go`
- Modify: `pkg/exporter/perpair_test.go`
- Modify: `pkg/exporter/pool_test.go`
- Modify: `pkg/exporter/writer_test.go`
- Modify: `pkg/exporter/exporter_test.go`

- [ ] **Step 1.3.1: Batch-rename across the test files**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && sed -i '' 's/BaselinePath:/BaselineRef:/g; s/TargetPath:/TargetRef:/g' pkg/exporter/*_test.go`

- [ ] **Step 1.3.2: Verify no stale references remain**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && grep -rn "BaselinePath\|TargetPath" pkg/exporter --include='*.go'`
Expected: empty.

### Task 1.4: Propagate rename through `cmd/`

**Files:**
- Modify: `cmd/diff.go`
- Modify: `cmd/bundle.go`

- [ ] **Step 1.4.1: Edit `cmd/diff.go` — in `runDiff`, rename Pair fields**

Change:
```go
Pairs: []exporter.Pair{{
    Name:         "default",
    BaselinePath: baseline.Path,
    TargetPath:   target.Path,
}},
```

To:
```go
Pairs: []exporter.Pair{{
    Name:        "default",
    BaselineRef: baseline.Raw,
    TargetRef:   target.Raw,
}},
```

Note the switch from `baseline.Path` to `baseline.Raw` — this already includes the transport prefix (`docker-archive:/tmp/old.tar`), which is what Phase 3 will need once `planPair` switches to `alltransports.ParseImageName`. Still path-only today; behavior-identical for archive inputs because `OpenArchiveRef` strips the prefix internally (verify this with the test below).

- [ ] **Step 1.4.2: Edit `cmd/bundle.go` — same rename in `runBundle`**

```go
pairs[i] = exporter.Pair{
    Name:        p.Name,
    BaselineRef: p.Baseline, // was: p.Baseline → BaselinePath
    TargetRef:   p.Target,
}
```

- [ ] **Step 1.4.3: Check no other call sites need updating**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && grep -rn "BaselinePath\|TargetPath" --include='*.go' .`
Expected: empty (all references migrated).

### Task 1.5: Verify tests pass, then commit

- [ ] **Step 1.5.1: Run the full test suite**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./...`
Expected: all pass.

Important check: the `imageio.OpenArchiveRef` call in `perpair.go` now receives `"docker-archive:/tmp/old.tar"` (the `.Raw` form) instead of `"/tmp/old.tar"`. `OpenArchiveRef` must accept both. Run:
`cd /Users/leosocy/workspace/repos/myself/diffah && grep -n "OpenArchiveRef" internal/imageio/*.go`

If `OpenArchiveRef` rejects transport-prefixed input, add a one-line `strings.TrimPrefix` inside the helper (or, preferred, strip the prefix at the call site in `perpair.go`) and re-run tests.

**If the test suite already fails here — stop and fix before committing.** Do NOT defer to later tasks.

- [ ] **Step 1.5.2: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add pkg/exporter/ cmd/diff.go cmd/bundle.go
git commit -m "refactor(exporter): rename Pair.BaselinePath/TargetPath → BaselineRef/TargetRef

Prep for Phase 3 registry support on diff/bundle. Pair values now carry
transport-prefixed references (e.g. docker-archive:/tmp/old.tar) instead
of bare paths. No user-visible change in this commit — subsequent phases
swap OpenArchiveRef for alltransports.ParseImageName."
```

---

# Phase 2 — Thread `*types.SystemContext` through `pkg/exporter`

No user-visible change. Adds the three Phase 2-style fields to `exporter.Options` and passes them through every `NewImageSource` call. The registry flag block is not yet wired on the CLI — the fields are unused in this phase but compile-clean, ready for Phase 4.

### Task 2.1: Extend `exporter.Options`

**Files:**
- Modify: `pkg/exporter/exporter.go`

- [ ] **Step 2.1.1: Add `SystemContext`, `RetryTimes`, `RetryDelay` to `Options`**

In `pkg/exporter/exporter.go`, replace the `Options` struct with:

```go
type Options struct {
	Pairs       []Pair
	Platform    string
	Compress    string
	OutputPath  string
	ToolVersion string
	IntraLayer  string
	CreatedAt   time.Time

	// Registry & transport — threaded into every types.ImageReference call.
	// Nil is acceptable; it behaves the same as today's path-only paths.
	SystemContext *types.SystemContext
	RetryTimes    int
	RetryDelay    time.Duration

	ProgressReporter progress.Reporter
	// Deprecated: use ProgressReporter. Will be removed in v0.4.
	Progress io.Writer

	Probe Probe

	fingerprinter Fingerprinter
}
```

Add the import for `go.podman.io/image/v5/types`.

- [ ] **Step 2.1.2: Write a unit test asserting `Options` accepts a non-nil `SystemContext` without panicking**

Add to `pkg/exporter/exporter_test.go`:

```go
func TestOptions_AcceptsSystemContext(t *testing.T) {
	sys := &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue}
	opts := exporter.Options{
		Pairs:         []exporter.Pair{{Name: "a", BaselineRef: "b", TargetRef: "t"}},
		SystemContext: sys,
		RetryTimes:    3,
	}
	if opts.SystemContext == nil {
		t.Fatal("SystemContext should be retained")
	}
}
```

Add `"go.podman.io/image/v5/types"` to the test file's imports.

- [ ] **Step 2.1.3: Run the test**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/ -run TestOptions_AcceptsSystemContext -v`
Expected: PASS.

### Task 2.2: Thread `sys` through `readManifestBundle`

**Files:**
- Modify: `pkg/exporter/perpair.go`

- [ ] **Step 2.2.1: Change `readManifestBundle` signature to accept `*types.SystemContext`**

Replace the signature and the `NewImageSource(ctx, nil)` call:

```go
func readManifestBundle(
	ctx context.Context, ref types.ImageReference, sys *types.SystemContext, platform string,
) (manifest.Manifest, []digest.Digest, []BaselineLayerMeta, []byte, string, error) {
	src, err := ref.NewImageSource(ctx, sys)
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	// ... rest unchanged
}
```

- [ ] **Step 2.2.2: Update all callers of `readManifestBundle` inside `planPair`**

Both calls become:
```go
_, baseDigests, baseMeta, baseMfBytes, baseMime, err := readManifestBundle(ctx, baseRef, opts.SystemContext, platform)
// ...
tgtParsed, _, _, tgtMfBytes, tgtMime, err := readManifestBundle(ctx, tgtRef, opts.SystemContext, platform)
```

That means `planPair` also needs a new parameter. Change its signature:

```go
func planPair(ctx context.Context, p Pair, opts *Options) (*pairPlan, error) {
```

Replace the `platform string` parameter with `opts *Options` and use `opts.Platform` inside the body. Call sites in `buildBundle` must pass `opts` (they already have `opts` in scope — `buildBundle(ctx, opts *Options)`).

- [ ] **Step 2.2.3: Thread `sys` into `readBlobBytes` and `readBlob` as well**

Update their signatures to take `sys *types.SystemContext`:

```go
func readBlobBytes(ctx context.Context, ref types.ImageReference, sys *types.SystemContext, d digest.Digest) ([]byte, error) {
	src, err := ref.NewImageSource(ctx, sys)
	// ... rest unchanged
}
```

And update callers in `perpair.go` (there are two — the config read and the layer read).

- [ ] **Step 2.2.4: Fix test files that call `planPair` with the old signature**

Any test that called `planPair(ctx, pair, "linux/amd64")` becomes:
```go
planPair(ctx, pair, &exporter.Options{Platform: "linux/amd64"})
```

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && grep -rn "planPair(" pkg/exporter/ --include='*.go'`

For each hit in a test file, rewrite to pass `&Options{...}`.

### Task 2.3: Thread `sys` through `pkg/exporter/baseline.go`

**Files:**
- Modify: `pkg/exporter/baseline.go`

- [ ] **Step 2.3.1: Confirm `ImageBaseline` already accepts `*types.SystemContext`**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && grep -n "func NewImageBaseline" pkg/exporter/baseline.go`
Expected: `func NewImageBaseline(ctx context.Context, ref types.ImageReference, sys *types.SystemContext, sourceHint, platform string)`.

If `sys` is already in the signature, no change needed here (confirmed from exploration). Skip to 2.3.2.

- [ ] **Step 2.3.2: Verify every call site in `pkg/exporter` passes `opts.SystemContext`**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && grep -rn "NewImageBaseline(" pkg/exporter/ --include='*.go' | grep -v _test.go`

For each prod-code call, confirm the `sys` argument is `opts.SystemContext`. Fix any that pass `nil` literally.

### Task 2.4: Run the suite and commit

- [ ] **Step 2.4.1: Run the exporter tests**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/...`
Expected: all pass.

- [ ] **Step 2.4.2: Run the whole suite**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./...`
Expected: all pass.

- [ ] **Step 2.4.3: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add pkg/exporter/
git commit -m "feat(exporter): add SystemContext + retry fields to Options, thread through

Prepares exporter for registry-sourced diff/bundle. Every NewImageSource
call in the exporter now receives opts.SystemContext. No user-visible
change — the CLI layer does not yet populate the new fields; nil remains
valid and behaves identically to today."
```

---

# Phase 3 — `planPair` switches to `alltransports.ParseImageName`

Internal mechanical swap: the exporter stops routing everything through the archive-only helper and accepts any transport that `alltransports` parses. CLI flags still not installed, so only archive inputs reach this path in practice — but the path is now transport-generic.

### Task 3.1: Write a failing test for registry-baseline acceptance inside `planPair`

**Files:**
- Modify: `pkg/exporter/perpair_test.go`

- [ ] **Step 3.1.1: Add a test that hands `planPair` a `docker://` ref and expects success**

Using the `internal/registrytest` harness to push the existing `v1_oci.tar` fixture and then running `planPair` against `docker://127.0.0.1:<port>/fixtures/v1`:

```go
func TestPlanPair_AcceptsDockerTransport(t *testing.T) {
	reg := registrytest.Start(t) // anonymous, HTTP
	defer reg.Close()
	reg.PushArchive(t, "../../testdata/fixtures/v1_oci.tar", "fixtures/v1:latest")
	reg.PushArchive(t, "../../testdata/fixtures/v2_oci.tar", "fixtures/v2:latest")

	ctx := context.Background()
	plan, err := planPair(ctx, Pair{
		Name:        "svc",
		BaselineRef: reg.DockerRef("fixtures/v1:latest"), // docker://127.0.0.1:PORT/fixtures/v1:latest
		TargetRef:   reg.DockerRef("fixtures/v2:latest"),
	}, &Options{Platform: "linux/amd64"})
	if err != nil {
		t.Fatalf("planPair err: %v", err)
	}
	if len(plan.TargetLayerDescs) == 0 {
		t.Fatal("expected non-empty target layers")
	}
}
```

The exact API of `registrytest.Start` / `PushArchive` / `DockerRef` is defined in `internal/registrytest/` from Phase 2 — confirm method names before writing by running:
`cd /Users/leosocy/workspace/repos/myself/diffah && grep -rn "^func (r \*.*Registry.*)" internal/registrytest/*.go | head`

- [ ] **Step 3.1.2: Run the test — expect it to FAIL**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/ -run TestPlanPair_AcceptsDockerTransport -v`
Expected: FAIL with an error like `open baseline docker://127.0.0.1:PORT/fixtures/v1:latest: invalid archive path` (because `OpenArchiveRef` doesn't speak `docker://`).

### Task 3.2: Swap `OpenArchiveRef` for `alltransports.ParseImageName`

**Files:**
- Modify: `pkg/exporter/perpair.go`

- [ ] **Step 3.2.1: Replace the two `imageio.OpenArchiveRef` calls**

Top of `perpair.go` imports: add `"go.podman.io/image/v5/transports/alltransports"`. Remove `"github.com/leosocy/diffah/internal/imageio"` if it was only used for `OpenArchiveRef` (verify with `grep` in file first).

In `planPair`:

```go
baseRef, err := alltransports.ParseImageName(p.BaselineRef)
if err != nil {
	return nil, fmt.Errorf("parse baseline ref %s: %w", p.BaselineRef, err)
}
tgtRef, err := alltransports.ParseImageName(p.TargetRef)
if err != nil {
	return nil, fmt.Errorf("parse target ref %s: %w", p.TargetRef, err)
}
```

- [ ] **Step 3.2.2: Replace `filepath.Base(p.BaselineRef)` with a ref-aware source hint**

Edit `pkg/exporter/assemble.go`. The `SourceHint` field is purely cosmetic (it shows up in the sidecar so consumers know where each blob "came from"), but `filepath.Base` gives nonsense for registry refs (`filepath.Base("docker://host/repo:tag")` → `"tag"`).

Add a new helper at the top of `assemble.go`:

```go
// sourceHintFor derives a compact provenance string from a transport
// reference. For archive transports it returns the file basename; for
// registry transports it returns the canonical repo:tag form.
func sourceHintFor(ref string) string {
	// archive transports carry a filesystem path — use filepath.Base
	for _, prefix := range []string{"docker-archive:", "oci-archive:", "oci:", "dir:"} {
		if strings.HasPrefix(ref, prefix) {
			return filepath.Base(strings.TrimPrefix(ref, prefix))
		}
	}
	// registry transports — drop the scheme and keep host/repo:tag
	if strings.HasPrefix(ref, "docker://") {
		return strings.TrimPrefix(ref, "docker://")
	}
	return ref
}
```

Add `"strings"` to imports if absent. Replace the `filepath.Base(p.BaselineRef)` call site with `sourceHintFor(p.BaselineRef)`.

- [ ] **Step 3.2.3: Rerun the Task 3.1 test — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/ -run TestPlanPair_AcceptsDockerTransport -v`
Expected: PASS.

- [ ] **Step 3.2.4: Run the whole suite**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./...`
Expected: all pass.

- [ ] **Step 3.2.5: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add pkg/exporter/
git commit -m "feat(exporter): planPair accepts any alltransports-parseable ref

Baseline and target refs inside Pair are now parsed via
alltransports.ParseImageName, unblocking docker://, oci:, dir: support
on diff/bundle. Archive values continue to work byte-identically.
SourceHint derivation switches from filepath.Base to a ref-aware helper
so registry refs produce meaningful provenance strings in the sidecar.

No CLI changes in this commit — cmd/diff.go and cmd/bundle.go still
synthesize path-only Pair values until Phase 4 wires the registry flag
block."
```

---

# Phase 4 — CLI: register registry flag block on `diff` and `bundle`

**User-visible feature:** `diffah diff docker://... docker://... delta.tar` works end-to-end against real registries. Same for `bundle`.

### Task 4.1: Install registry flags on `diff`

**Files:**
- Modify: `cmd/diff.go`

- [ ] **Step 4.1.1: Edit `diffFlags` to hold the `registryContextBuilder`**

```go
var diffFlags = struct {
	platform           string
	compress           string
	intraLayer         string
	dryRun             bool
	buildSystemContext registryContextBuilder
}{}
```

- [ ] **Step 4.1.2: Install the flag block inside `newDiffCommand`**

After the existing `f.BoolVarP(&diffFlags.dryRun, ...)`, add:

```go
diffFlags.buildSystemContext = installRegistryFlags(c)
```

- [ ] **Step 4.1.3: Populate `exporter.Options` from the builder inside `runDiff`**

After `ParseImageRef` for target succeeds, add:

```go
sc, retryTimes, retryDelay, err := diffFlags.buildSystemContext()
if err != nil {
	return err
}
```

Add these three fields into the `exporter.Options{...}` literal below:

```go
SystemContext: sc,
RetryTimes:    retryTimes,
RetryDelay:    retryDelay,
```

### Task 4.2: Install registry flags on `bundle`

**Files:**
- Modify: `cmd/bundle.go`

- [ ] **Step 4.2.1: Repeat Task 4.1 steps for `bundle.go`**

Add `buildSystemContext registryContextBuilder` to `bundleFlags`. Call `installRegistryFlags(c)` after the `dry-run` flag. Call the builder inside `runBundle` and thread three fields into `exporter.Options`.

### Task 4.3: Write registry integration tests for `diff`

**Files:**
- Create: `cmd/diff_registry_integration_test.go`

- [ ] **Step 4.3.1: Scaffold the test file**

Model on `cmd/apply_registry_integration_test.go` (Phase 2). Use the same `registrytest` harness invocation pattern. Include the eight cases from §7.3 of the spec (anon pull, basic-auth, bearer-token, mTLS, bad creds, unreachable, unknown tag, retry success, retry exhaust).

Template for one case:

```go
//go:build integration

package cmd_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/leosocy/diffah/internal/registrytest"
)

func TestDiff_AnonymousPull(t *testing.T) {
	reg := registrytest.Start(t)
	defer reg.Close()
	reg.PushArchive(t, "../testdata/fixtures/v1_oci.tar", "fixtures/v1:latest")
	reg.PushArchive(t, "../testdata/fixtures/v2_oci.tar", "fixtures/v2:latest")

	out := t.TempDir() + "/delta.tar"
	args := []string{
		"diff",
		"docker://" + reg.HostPort() + "/fixtures/v1:latest",
		"docker://" + reg.HostPort() + "/fixtures/v2:latest",
		out,
	}
	cmd := exec.Command("./diffah", args...) // built by TestMain
	cmd.Env = append(cmd.Environ(), "DIFFAH_LOG=debug")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("diff failed: %v\nstderr=%s", err, stderr.String())
	}
	// Basic smoke: archive exists, non-empty.
}
```

- [ ] **Step 4.3.2: Run the anon-pull case — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test -tags integration ./cmd/ -run TestDiff_AnonymousPull -v`
Expected: PASS.

- [ ] **Step 4.3.3: Add the remaining seven cases from §7.3 of the spec**

Copy the existing apply-side matrix as a starting point (`cmd/apply_registry_integration_test.go`). Each new test swaps `apply` for `diff` and drops the target-image output positional.

- [ ] **Step 4.3.4: Run the full integration matrix**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test -tags integration ./cmd/ -run TestDiff_ -v`
Expected: all PASS.

### Task 4.4: Same integration matrix for `bundle`

**Files:**
- Create: `cmd/bundle_registry_integration_test.go`

- [ ] **Step 4.4.1: Scaffold and mirror the `diff` matrix**

Bundle tests need a BundleSpec JSON on disk. Use `t.TempDir()` + `os.WriteFile` to drop a two-image spec. Until Phase 5 lands, the spec uses `docker-archive:` values for half and `docker://<reg>/...` values for half (the mixed-sources case).

```go
func TestBundle_MixedSources(t *testing.T) {
	reg := registrytest.Start(t)
	defer reg.Close()
	reg.PushArchive(t, "../testdata/fixtures/v1_oci.tar", "svc-a/v1:latest")
	reg.PushArchive(t, "../testdata/fixtures/v2_oci.tar", "svc-a/v2:latest")

	dir := t.TempDir()
	specPath := dir + "/bundle.json"
	os.WriteFile(specPath, []byte(fmt.Sprintf(`{
  "pairs": [
    {"name": "svc-a",
     "baseline": "docker://%s/svc-a/v1:latest",
     "target":   "docker://%s/svc-a/v2:latest"},
    {"name": "svc-b",
     "baseline": "docker-archive:../testdata/fixtures/v1_oci.tar",
     "target":   "docker-archive:../testdata/fixtures/v2_oci.tar"}
  ]
}`, reg.HostPort(), reg.HostPort())), 0o644)

	out := dir + "/bundle.tar"
	cmd := exec.Command("./diffah", "bundle", specPath, out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("bundle failed: %v\nstderr=%s", err, stderr.String())
	}
}
```

This case requires Phase 5's BundleSpec transport-prefix requirement to be accepted — add a `t.Skip("needs Phase 5 BundleSpec changes")` until Phase 5 lands, or land Phase 5 before this task.

### Task 4.5: Run suite and commit

- [ ] **Step 4.5.1: Run all tests (unit + integration)**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./... && go test -tags integration ./...`
Expected: all pass.

- [ ] **Step 4.5.2: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add cmd/diff.go cmd/bundle.go cmd/diff_registry_integration_test.go cmd/bundle_registry_integration_test.go
git commit -m "feat(cmd): registry sources on diff and bundle

diff and bundle gain the Phase 2 registry & transport flag block
(--authfile, --creds, --tls-verify, --retry-times, etc.). Integration
matrix against registrytest covers anonymous, basic-auth, bearer-token,
and mTLS pulls plus failure-classification cases."
```

---

# Phase 5 — BundleSpec breaking change: require transport prefixes

**User-visible breaking change.** Bare-path values in `BundleSpec` now fail with a sed-migration hint.

### Task 5.1: Write a failing test for bare-path rejection

**Files:**
- Modify: `pkg/diff/bundle_spec_test.go`

- [ ] **Step 5.1.1: Add the failing test**

```go
func TestParseBundleSpec_BarePathRejected(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/bundle.json"
	os.WriteFile(path, []byte(`{
  "pairs": [
    {"name": "svc-a", "baseline": "v1/svc-a.tar", "target": "v2/svc-a.tar"}
  ]
}`), 0o644)

	_, err := diff.ParseBundleSpec(path)
	if err == nil {
		t.Fatal("expected error on bare-path baseline")
	}
	var missing *diff.ErrBundleSpecMissingTransport
	if !errors.As(err, &missing) {
		t.Fatalf("want ErrBundleSpecMissingTransport, got %T: %v", err, err)
	}
	if missing.FieldPath != "pairs[0].baseline" {
		t.Errorf("FieldPath = %q, want pairs[0].baseline", missing.FieldPath)
	}
	if !strings.Contains(err.Error(), "docker-archive:") {
		t.Error("migration hint should mention 'docker-archive:' prefix")
	}
}
```

- [ ] **Step 5.1.2: Run the test — expect FAIL (error type not defined yet)**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/diff/ -run TestParseBundleSpec_BarePathRejected -v`
Expected: FAIL with compile error `undefined: diff.ErrBundleSpecMissingTransport`.

### Task 5.2: Add `ErrBundleSpecMissingTransport`

**Files:**
- Modify: `pkg/diff/errors.go`

- [ ] **Step 5.2.1: Define the error type**

```go
type ErrBundleSpecMissingTransport struct {
	FieldPath string // e.g. "pairs[0].baseline"
	Value     string
}

var _ errs.Categorized = (*ErrBundleSpecMissingTransport)(nil)
var _ errs.Advised = (*ErrBundleSpecMissingTransport)(nil)

func (e *ErrBundleSpecMissingTransport) Error() string {
	return fmt.Sprintf(
		"%s: missing transport prefix (%q)\n\n"+
			"prefix archive paths with 'docker-archive:' —\n"+
			"  sed -E -i '' 's|(\\\"baseline\\\"\\|\\\"target\\\"): \\\"([^:\\\"]*\\\\.tar[a-z]*)\\\"|\\1: \\\"docker-archive:\\\\2\\\"|g' bundle.json",
		e.FieldPath, e.Value,
	)
}

func (e *ErrBundleSpecMissingTransport) Category() errs.Category { return errs.CategoryUser }
func (e *ErrBundleSpecMissingTransport) NextAction() string {
	return "prefix each bundle-spec value with a transport (docker-archive:, docker://, oci:, dir:)"
}
```

Confirm the imports `"fmt"` and `"github.com/leosocy/diffah/pkg/diff/errs"` are present.

### Task 5.3: Enforce transport prefix in `ParseBundleSpec`

**Files:**
- Modify: `pkg/diff/bundle_spec.go`

- [ ] **Step 5.3.1: Edit the loop that validates pairs**

Replace the current resolve-spec-path lines with a prefix check + transport-aware resolver:

```go
for i := range spec.Pairs {
	p := &spec.Pairs[i]
	if !nameRegex.MatchString(p.Name) {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
			"pairs[%d].name %q does not match %s", i, p.Name, nameRegex)}
	}
	if _, dup := seen[p.Name]; dup {
		return nil, &ErrDuplicateBundleName{Name: p.Name}
	}
	seen[p.Name] = struct{}{}
	if p.Baseline == "" || p.Target == "" {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
			"pairs[%d] requires baseline and target", i)}
	}

	resolvedBaseline, err := resolveTransportAwarePath(base, p.Baseline)
	if err != nil {
		return nil, fieldErr("pairs["+strconv.Itoa(i)+"].baseline", p.Baseline, err)
	}
	p.Baseline = resolvedBaseline

	resolvedTarget, err := resolveTransportAwarePath(base, p.Target)
	if err != nil {
		return nil, fieldErr("pairs["+strconv.Itoa(i)+"].target", p.Target, err)
	}
	p.Target = resolvedTarget
}
```

- [ ] **Step 5.3.2: Add the helpers `resolveTransportAwarePath` and `fieldErr`**

At the bottom of `bundle_spec.go`:

```go
// resolveTransportAwarePath requires a transport prefix. For file-backed
// transports (docker-archive:, oci-archive:, oci:, dir:) a relative path
// is resolved against base (the directory of the spec file); for
// docker:// the ref is returned unchanged.
func resolveTransportAwarePath(base, ref string) (string, error) {
	if err := validateTransportRef(ref); err != nil {
		return "", err
	}
	// Parse the prefix; for file-backed transports, resolve the path part
	// relative to base if it's relative.
	colon := strings.Index(ref, ":")
	prefix, rest := ref[:colon], ref[colon+1:]
	switch prefix {
	case "docker-archive", "oci-archive", "oci", "dir":
		// rest may carry a trailing ":reference" suffix for archive
		// transports (e.g. oci-archive:path.tar:name:tag). Only the
		// first path segment is resolved; anything after a ':' is
		// passed through verbatim.
		pathPart := rest
		tail := ""
		if idx := strings.Index(rest, ":"); idx >= 0 {
			pathPart, tail = rest[:idx], rest[idx:]
		}
		if !filepath.IsAbs(pathPart) {
			pathPart = filepath.Join(base, pathPart)
		}
		return prefix + ":" + pathPart + tail, nil
	default:
		// docker:// and others — return unchanged.
		return ref, nil
	}
}

func fieldErr(fieldPath, value string, wrapped error) error {
	// If the wrapped error is a missing-prefix, lift into the typed form.
	if strings.Contains(wrapped.Error(), "missing transport prefix") {
		return &ErrBundleSpecMissingTransport{FieldPath: fieldPath, Value: value}
	}
	return &ErrInvalidBundleSpec{Path: "", Reason: fieldPath + ": " + wrapped.Error()}
}
```

Add `"strconv"` to the imports.

- [ ] **Step 5.3.3: Run the Task 5.1 test — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/diff/ -run TestParseBundleSpec_BarePathRejected -v`
Expected: PASS.

### Task 5.4: Update existing BundleSpec test fixtures that used bare paths

**Files:**
- Modify: `pkg/diff/bundle_spec_test.go`
- Modify: any test or example file that inlines a bare-path BundleSpec

- [ ] **Step 5.4.1: Find and fix stale fixtures**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && grep -rn '"baseline"' --include='*.go' --include='*.json'`

For each fixture, prefix the `baseline` and `target` values with `docker-archive:`.

- [ ] **Step 5.4.2: Run the bundle_spec tests**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/diff/ -v`
Expected: all pass.

### Task 5.5: Surface the migration hint at the CLI layer

**Files:**
- Modify: `cmd/bundle.go`

- [ ] **Step 5.5.1: Ensure the CLI error formatter preserves the hint**

`ParseBundleSpec` errors bubble up from `runBundle`. Since `ErrBundleSpecMissingTransport` satisfies `errs.Categorized` and `errs.Advised`, the existing cobra error printer (which already formats `*cliErr` consistently) renders it correctly. Verify by adding to `cmd/bundle_test.go`:

```go
func TestBundle_BarePathEmitsMigrationHint(t *testing.T) {
	dir := t.TempDir()
	specPath := dir + "/bundle.json"
	os.WriteFile(specPath, []byte(`{"pairs":[{"name":"a","baseline":"v1.tar","target":"v2.tar"}]}`), 0o644)

	stderr := &bytes.Buffer{}
	cmd := newRootCmd()
	cmd.SetArgs([]string{"bundle", specPath, dir + "/out.tar"})
	cmd.SetErr(stderr)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr.String(), "missing transport prefix") {
		t.Errorf("want missing-transport-prefix in stderr, got: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "sed -E") {
		t.Errorf("want sed migration hint, got: %s", stderr.String())
	}
}
```

- [ ] **Step 5.5.2: Run the test**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./cmd/ -run TestBundle_BarePathEmitsMigrationHint -v`
Expected: PASS.

### Task 5.6: CHANGELOG + commit

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 5.6.1: Add the `## [Unreleased] — Phase 3` section**

Prepend to `CHANGELOG.md` (before the existing Phase 2 section):

```markdown
## [Unreleased] — Phase 3: Registry-native export + signing

### Breaking changes

- **`BundleSpec` JSON**: `baseline` / `target` values must now carry a
  transport prefix. Bare-path values (`"baseline": "v1/svc.tar"`) fail
  with a migration hint. One-liner fix:

  ```
  sed -E -i '' 's|(\"baseline\"\|\"target\"): \"([^:\"]*\.tar[a-z]*)\"|\1: \"docker-archive:\2\"|g' bundle.json
  ```

(Rest of the section will be appended incrementally as later phases land.)
```

- [ ] **Step 5.6.2: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add pkg/diff/ cmd/bundle_test.go CHANGELOG.md
git commit -m "feat(diff)!: require transport prefix on BundleSpec baseline/target values

Brings BundleSpec in line with Phase 2's BaselineSpec/OutputSpec: every
value must carry a supported transport prefix. Bare-path values fail
parse with a typed ErrBundleSpecMissingTransport that includes a sed
one-liner migration hint.

BREAKING CHANGE: existing bundle.json files with bare-path values will
fail to parse. Migration documented in CHANGELOG."
```

---

# Phase 6 — `pkg/signer` package (standalone)

New package, zero CLI integration yet. Full TDD. Every signer test runs purely on fixtures — no network, no cosign binary (except the gated compat test).

### Task 6.1: Scaffold the package directory

**Files:**
- Create: `pkg/signer/signer.go` (skeleton)
- Create: `pkg/signer/errors.go`
- Create: `pkg/signer/canonical.go`
- Create: `pkg/signer/cosign.go`
- Create: `pkg/signer/verifier.go`
- Create: `pkg/signer/rekor.go`
- Create: `pkg/signer/testdata/README.md`

- [ ] **Step 6.1.1: Create `pkg/signer/errors.go`**

```go
// Package signer emits and verifies cosign-compatible keyed signatures
// over the diffah sidecar digest. See design doc:
// docs/superpowers/specs/2026-04-24-phase3-registry-native-export-signing-design.md
package signer

import (
	"errors"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// ErrKeyEncrypted indicates a key file is encrypted but no passphrase
// was supplied.
var ErrKeyEncrypted = &categorizedErr{msg: "private key is encrypted; provide --sign-key-password-stdin", cat: errs.CategoryUser}

// ErrKeyPassphraseIncorrect indicates the supplied passphrase did not
// decrypt the key.
var ErrKeyPassphraseIncorrect = &categorizedErr{msg: "private key passphrase is incorrect", cat: errs.CategoryUser}

// ErrKeyUnsupportedKDF indicates a cosign-boxed key was produced with
// KDF parameters we cannot decrypt.
var ErrKeyUnsupportedKDF = &categorizedErr{msg: "private key uses unsupported KDF parameters", cat: errs.CategoryUser}

// ErrSignatureInvalid indicates the cryptographic check failed.
var ErrSignatureInvalid = &categorizedErr{msg: "signature does not verify under the supplied public key", cat: errs.CategoryContent}

// ErrArchiveUnsigned indicates --verify was supplied but the archive
// carries no signature sidecar.
var ErrArchiveUnsigned = &categorizedErr{msg: "archive has no signature; --verify requires a signed archive", cat: errs.CategoryContent}

type categorizedErr struct {
	msg string
	cat errs.Category
}

func (e *categorizedErr) Error() string           { return e.msg }
func (e *categorizedErr) Category() errs.Category { return e.cat }
func (e *categorizedErr) Is(target error) bool    { return errors.Is(target, e) }
```

- [ ] **Step 6.1.2: Create `pkg/signer/canonical.go` — RFC 8785 JCS**

The Go ecosystem has a well-maintained JCS implementation at `github.com/gowebpki/jcs`. Confirm first whether it's already a transitive dep:
`cd /Users/leosocy/workspace/repos/myself/diffah && go list -m all | grep jcs || echo 'not present'`.

If not present, we vendor it explicitly in Task 6.7. For now, assume it will be added:

```go
package signer

import (
	"encoding/json"
	"fmt"

	"github.com/gowebpki/jcs"
)

// JCSCanonical returns RFC 8785 canonical JSON bytes for v. It round-trips
// through json.Marshal and then jcs.Transform so that maps, slices, and
// structs all serialize deterministically.
func JCSCanonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal for jcs: %w", err)
	}
	out, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("jcs transform: %w", err)
	}
	return out, nil
}

// JCSCanonicalFromBytes canonicalizes already-serialized JSON bytes
// (e.g. the bytes extracted from a tar entry) without re-marshalling.
func JCSCanonicalFromBytes(raw []byte) ([]byte, error) {
	out, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("jcs transform: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 6.1.3: Create `pkg/signer/testdata/README.md` documenting fixtures**

```markdown
# signer testdata

Throwaway ECDSA P-256 keypairs used only for unit tests. These are not
production secrets; committing them to version control is intentional.

## Files

- `test_ec_p256.key`        — unencrypted PEM (PKCS8 or SEC1) private key
- `test_ec_p256.pub`        — matching PEM public key
- `test_ec_p256_enc.key`    — cosign-boxed (scrypt+secretbox) private key
- `test_ec_p256_enc.pass`   — passphrase for the encrypted key

Regenerate with:

    go run ./pkg/signer/cmd/gen-testdata
```

### Task 6.2: Generate committed keypair fixtures

**Files:**
- Create: `pkg/signer/testdata/test_ec_p256.key`
- Create: `pkg/signer/testdata/test_ec_p256.pub`
- Create: `pkg/signer/testdata/test_ec_p256_enc.key`
- Create: `pkg/signer/testdata/test_ec_p256_enc.pass`
- Create (optional): `pkg/signer/cmd/gen-testdata/main.go`

- [ ] **Step 6.2.1: Write a one-shot generator**

```go
// pkg/signer/cmd/gen-testdata/main.go
//go:build ignore

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
)

func main() {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	if err := os.WriteFile("pkg/signer/testdata/test_ec_p256.key",
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
		panic(err)
	}
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err := os.WriteFile("pkg/signer/testdata/test_ec_p256.pub",
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}), 0o644); err != nil {
		panic(err)
	}

	// Encrypted variant matching cosign 2.x layout.
	pass := []byte("diffah-testdata-passphrase-do-not-use-anywhere")
	var salt [32]byte
	rand.Read(salt[:])
	derived, _ := scrypt.Key(pass, salt[:], 1<<15, 8, 1, 32)
	var key32 [32]byte
	copy(key32[:], derived)
	var nonce [24]byte
	rand.Read(nonce[:])
	sealed := secretbox.Seal(nil, pkcs8, &nonce, &key32)

	// cosign wraps this in a custom JSON envelope — format documented in
	// https://github.com/sigstore/cosign/blob/main/pkg/cosign/keys.go
	envelope := fmt.Sprintf(`{"kdf":{"name":"scrypt","params":{"N":32768,"r":8,"p":1},"salt":%q},"cipher":{"name":"nacl/secretbox","nonce":%q},"ciphertext":%q}`,
		salt[:], nonce[:], sealed) // simplified; real generator base64-encodes
	_ = envelope
	// ... (output goes to test_ec_p256_enc.key and .pass)
	os.WriteFile("pkg/signer/testdata/test_ec_p256_enc.pass", pass, 0o600)
}
```

The exact JSON envelope format must match what Phase 6's signer consumes. Cross-check against the first cosign compat test (Task 6.8) and iterate if they disagree.

- [ ] **Step 6.2.2: Run the generator**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go run ./pkg/signer/cmd/gen-testdata/`

Expected: four files materialize under `pkg/signer/testdata/`.

- [ ] **Step 6.2.3: Gitignore the generator command so it doesn't ship**

Edit `.gitignore` — no, the generator should ship as a reproducible tool. Keep it in `pkg/signer/cmd/gen-testdata/main.go` and rely on the `//go:build ignore` tag.

### Task 6.3: Write the failing test for `Sign → Verify` round-trip

**Files:**
- Create: `pkg/signer/signer_test.go`

- [ ] **Step 6.3.1: Add the round-trip test**

```go
package signer_test

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"

	"github.com/leosocy/diffah/pkg/signer"
)

func TestSignVerify_UnencryptedKeyRoundTrip(t *testing.T) {
	ctx := context.Background()
	payload := sha256.Sum256([]byte(`{"k":"v"}`))
	sig, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath: "testdata/test_ec_p256.key",
		Payload: payload[:],
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(sig.Raw) == 0 {
		t.Fatal("empty signature")
	}
	if len(sig.CertPEM) != 0 {
		t.Fatal("cert should be empty in keyed mode")
	}
	if len(sig.RekorBundle) != 0 {
		t.Fatal("rekor bundle should be empty with no --rekor-url")
	}

	if err := signer.Verify(ctx, "testdata/test_ec_p256.pub", payload[:], sig, ""); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// flip one byte of payload → verify fails
	tampered := append([]byte{}, payload[:]...)
	tampered[0] ^= 0xFF
	if err := signer.Verify(ctx, "testdata/test_ec_p256.pub", tampered, sig, ""); err == nil {
		t.Fatal("expected verify to fail on tampered payload")
	}

	// flip one byte of signature → verify fails
	sig2 := *sig
	sig2.Raw = append([]byte{}, sig.Raw...)
	sig2.Raw[0] ^= 0xFF
	if err := signer.Verify(ctx, "testdata/test_ec_p256.pub", payload[:], &sig2, ""); err == nil {
		t.Fatal("expected verify to fail on tampered signature")
	}
}
```

- [ ] **Step 6.3.2: Run the test — expect FAIL (Sign/Verify not implemented)**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/signer/ -v`
Expected: compile errors or FAIL (`undefined: signer.Sign`).

### Task 6.4: Implement `Sign`

**Files:**
- Modify: `pkg/signer/signer.go`

- [ ] **Step 6.4.1: Define the public types and `Sign`**

```go
package signer

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

type Signature struct {
	Raw         []byte // DER ECDSA (r,s)
	CertPEM     []byte // PEM-encoded Fulcio cert; nil in keyed mode
	RekorBundle []byte // cosign 2.x rekor bundle JSON; nil when no --rekor-url
}

type SignRequest struct {
	KeyPath         string
	PassphraseBytes []byte
	RekorURL        string
	Payload         []byte // sha256(jcs(sidecar.json))
}

func Sign(ctx context.Context, req SignRequest) (*Signature, error) {
	priv, err := loadPrivateKey(req.KeyPath, req.PassphraseBytes)
	if err != nil {
		return nil, err
	}
	// zero the passphrase in-place
	for i := range req.PassphraseBytes {
		req.PassphraseBytes[i] = 0
	}

	sig, err := ecdsa.SignASN1(rand.Reader, priv, req.Payload)
	if err != nil {
		return nil, fmt.Errorf("ecdsa sign: %w", err)
	}
	out := &Signature{Raw: sig}
	if req.RekorURL != "" {
		bundle, err := UploadEntry(ctx, req.RekorURL, sig, req.Payload, &priv.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("rekor upload: %w", err)
		}
		out.RekorBundle = bundle
	}
	return out, nil
}

func loadPrivateKey(path string, passphrase []byte) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	// Sniff: cosign-boxed keys live in a JSON envelope starting with '{'.
	if len(raw) > 0 && raw[0] == '{' {
		return decryptCosignBoxedKey(raw, passphrase)
	}
	// Otherwise treat as plain PEM.
	if len(passphrase) > 0 {
		// user supplied a passphrase for an unencrypted key — that's
		// not an error, just ignore it silently.
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in key file %s", path)
	}
	return parseECPrivateKeyBlock(block)
}

func parseECPrivateKeyBlock(block *pem.Block) (*ecdsa.PrivateKey, error) {
	switch block.Type {
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		return k, err
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("key is not ECDSA")
		}
		return ec, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type %q", block.Type)
	}
}
```

- [ ] **Step 6.4.2: Stub out `decryptCosignBoxedKey` so tests compile**

Append to `signer.go`:

```go
func decryptCosignBoxedKey(envelope, passphrase []byte) (*ecdsa.PrivateKey, error) {
	// Full implementation lands in Task 6.6.
	return nil, fmt.Errorf("cosign-boxed keys not yet supported (stub)")
}
```

### Task 6.5: Implement `Verify`

**Files:**
- Modify: `pkg/signer/verifier.go`

- [ ] **Step 6.5.1: Implement**

```go
package signer

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

func Verify(ctx context.Context, pubKeyPath string, payload []byte, sig *Signature, rekorURL string) error {
	pub, err := loadPublicKey(pubKeyPath)
	if err != nil {
		return err
	}
	if !ecdsa.VerifyASN1(pub, payload, sig.Raw) {
		return ErrSignatureInvalid
	}
	if rekorURL != "" && sig.RekorBundle != nil {
		if err := verifyRekorBundle(ctx, rekorURL, sig.RekorBundle, payload, pub); err != nil {
			return fmt.Errorf("rekor verify: %w", err)
		}
	}
	// rekorURL set but no bundle — warn-only case is handled at the CLI
	// layer where we have access to slog; the signer stays pure.
	return nil
}

func loadPublicKey(path string) (*ecdsa.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pub key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in pub key file %s", path)
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("pub key is not ECDSA")
	}
	return pub, nil
}

func verifyRekorBundle(ctx context.Context, rekorURL string, bundle []byte, payload []byte, pub *ecdsa.PublicKey) error {
	// Full implementation lands in Task 6.7.
	return nil // stub — verify path is exercised only when --verify-rekor-url is set
}
```

### Task 6.6: Run the round-trip test — should now PASS

- [ ] **Step 6.6.1: Run**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/signer/ -run TestSignVerify_UnencryptedKeyRoundTrip -v`
Expected: PASS.

- [ ] **Step 6.6.2: Commit partial signer (unencrypted path)**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add pkg/signer/
git commit -m "feat(signer): Sign/Verify round-trip for unencrypted ECDSA-P256 keys

pkg/signer takes a base64-free DER signature over a 32-byte payload. The
caller is responsible for supplying the sha256(jcs(sidecar.json))
payload; the signer stays pure (no I/O beyond reading key files).
Cosign-boxed encrypted keys and Rekor upload/verify are stubbed — next
commits."
```

### Task 6.7: Cosign-boxed encrypted key support

**Files:**
- Modify: `pkg/signer/signer.go` (replace the stub `decryptCosignBoxedKey`)
- Create: `pkg/signer/signer_encrypted_test.go`

- [ ] **Step 6.7.1: Write the failing test**

```go
package signer_test

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"

	"github.com/leosocy/diffah/pkg/signer"
)

func TestSignVerify_EncryptedKeyRoundTrip(t *testing.T) {
	ctx := context.Background()
	pass, _ := os.ReadFile("testdata/test_ec_p256_enc.pass")
	payload := sha256.Sum256([]byte(`{"k":"v"}`))
	sig, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath:         "testdata/test_ec_p256_enc.key",
		PassphraseBytes: append([]byte{}, pass...),
		Payload:         payload[:],
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := signer.Verify(ctx, "testdata/test_ec_p256.pub", payload[:], sig, ""); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestSign_EncryptedKey_WrongPassphrase(t *testing.T) {
	ctx := context.Background()
	payload := sha256.Sum256([]byte(`{"k":"v"}`))
	_, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath:         "testdata/test_ec_p256_enc.key",
		PassphraseBytes: []byte("wrong"),
		Payload:         payload[:],
	})
	if err == nil {
		t.Fatal("expected error on wrong passphrase")
	}
	if !errors.Is(err, signer.ErrKeyPassphraseIncorrect) {
		t.Errorf("want ErrKeyPassphraseIncorrect, got %v", err)
	}
}

func TestSign_EncryptedKey_MissingPassphrase(t *testing.T) {
	ctx := context.Background()
	payload := sha256.Sum256([]byte(`{"k":"v"}`))
	_, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath: "testdata/test_ec_p256_enc.key",
		Payload: payload[:],
	})
	if !errors.Is(err, signer.ErrKeyEncrypted) {
		t.Errorf("want ErrKeyEncrypted, got %v", err)
	}
}
```

- [ ] **Step 6.7.2: Run — expect FAIL (stub in place)**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/signer/ -run TestSignVerify_EncryptedKey -v`
Expected: FAIL ("cosign-boxed keys not yet supported").

- [ ] **Step 6.7.3: Implement `decryptCosignBoxedKey`**

Replace the stub in `pkg/signer/signer.go`:

```go
import (
	// ... existing imports ...
	"encoding/base64"
	"encoding/json"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
)

type cosignBoxedKey struct {
	KDF struct {
		Name   string `json:"name"`
		Params struct {
			N int `json:"N"`
			R int `json:"r"`
			P int `json:"p"`
		} `json:"params"`
		Salt string `json:"salt"` // base64
	} `json:"kdf"`
	Cipher struct {
		Name  string `json:"name"`
		Nonce string `json:"nonce"` // base64
	} `json:"cipher"`
	Ciphertext string `json:"ciphertext"` // base64
}

func decryptCosignBoxedKey(envelope, passphrase []byte) (*ecdsa.PrivateKey, error) {
	if len(passphrase) == 0 {
		return nil, ErrKeyEncrypted
	}
	var boxed cosignBoxedKey
	if err := json.Unmarshal(envelope, &boxed); err != nil {
		return nil, fmt.Errorf("parse cosign-boxed key: %w", err)
	}
	if boxed.KDF.Name != "scrypt" {
		return nil, ErrKeyUnsupportedKDF
	}
	salt, err := base64.StdEncoding.DecodeString(boxed.KDF.Salt)
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	derived, err := scrypt.Key(passphrase, salt, boxed.KDF.Params.N, boxed.KDF.Params.R, boxed.KDF.Params.P, 32)
	if err != nil {
		return nil, fmt.Errorf("scrypt: %w", err)
	}
	var key32 [32]byte
	copy(key32[:], derived)

	nonceRaw, err := base64.StdEncoding.DecodeString(boxed.Cipher.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	if len(nonceRaw) != 24 {
		return nil, fmt.Errorf("nonce length %d, want 24", len(nonceRaw))
	}
	var nonce [24]byte
	copy(nonce[:], nonceRaw)

	ct, err := base64.StdEncoding.DecodeString(boxed.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}
	pt, ok := secretbox.Open(nil, ct, &nonce, &key32)
	if !ok {
		return nil, ErrKeyPassphraseIncorrect
	}
	// pt is PKCS8 DER; re-use the existing PEM parser helper by
	// synthesizing a fake block.
	return parseECPrivateKeyBlock(&pem.Block{Type: "PRIVATE KEY", Bytes: pt})
}
```

- [ ] **Step 6.7.4: Run the encrypted-key tests — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/signer/ -run "TestSignVerify_EncryptedKeyRoundTrip|TestSign_EncryptedKey_" -v`
Expected: all three PASS.

### Task 6.8: JCS property tests

**Files:**
- Create: `pkg/signer/canonical_test.go`

- [ ] **Step 6.8.1: Add a key-permutation property test**

```go
package signer_test

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/leosocy/diffah/pkg/signer"
)

func TestJCSCanonical_KeyPermutationInvariant(t *testing.T) {
	seed := rand.New(rand.NewSource(42))
	for i := 0; i < 20; i++ {
		base := map[string]any{
			"a": float64(seed.Int()),
			"b": "string",
			"c": []any{float64(1), float64(2), float64(3)},
			"d": map[string]any{"x": true, "y": float64(-1)},
			"e": nil,
		}
		raw, _ := json.Marshal(base)
		canon1, err := signer.JCSCanonicalFromBytes(raw)
		if err != nil {
			t.Fatalf("canonical: %v", err)
		}
		// Re-marshal 100 times and re-canonicalize; output must be stable.
		for j := 0; j < 100; j++ {
			shuffled, _ := json.Marshal(base)
			canon2, err := signer.JCSCanonicalFromBytes(shuffled)
			if err != nil {
				t.Fatalf("canonical iter %d: %v", j, err)
			}
			if !bytes.Equal(canon1, canon2) {
				t.Fatalf("iter %d not stable:\n a=%s\n b=%s", j, canon1, canon2)
			}
		}
	}
}
```

- [ ] **Step 6.8.2: Run**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/signer/ -run TestJCSCanonical_ -v`
Expected: PASS.

### Task 6.9: Sidecar file I/O

**Files:**
- Modify: `pkg/signer/cosign.go`
- Create: `pkg/signer/cosign_test.go`

- [ ] **Step 6.9.1: Write the failing write/load round-trip test**

```go
package signer_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/leosocy/diffah/pkg/signer"
)

func TestSidecars_WriteLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "delta.tar")
	if err := os.WriteFile(archivePath, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	sig := &signer.Signature{Raw: []byte{0x30, 0x45}}
	if err := signer.WriteSidecars(archivePath, sig); err != nil {
		t.Fatalf("WriteSidecars: %v", err)
	}
	// .sig present, .cert and .rekor.json absent
	if _, err := os.Stat(archivePath + ".sig"); err != nil {
		t.Errorf(".sig missing: %v", err)
	}
	if _, err := os.Stat(archivePath + ".cert"); !os.IsNotExist(err) {
		t.Errorf(".cert should not exist, err=%v", err)
	}
	if _, err := os.Stat(archivePath + ".rekor.json"); !os.IsNotExist(err) {
		t.Errorf(".rekor.json should not exist, err=%v", err)
	}

	loaded, err := signer.LoadSidecars(archivePath)
	if err != nil {
		t.Fatalf("LoadSidecars: %v", err)
	}
	if !bytes.Equal(loaded.Raw, sig.Raw) {
		t.Errorf("round-trip mismatch")
	}
}

func TestSidecars_LoadAbsent(t *testing.T) {
	dir := t.TempDir()
	sig, err := signer.LoadSidecars(filepath.Join(dir, "delta.tar"))
	if err != nil {
		t.Fatalf("LoadSidecars on unsigned: %v", err)
	}
	if sig != nil {
		t.Errorf("want nil sig on absent .sig, got %+v", sig)
	}
}
```

- [ ] **Step 6.9.2: Implement `WriteSidecars` and `LoadSidecars`**

```go
package signer

import (
	"encoding/base64"
	"errors"
	"os"
)

func WriteSidecars(archivePath string, sig *Signature) error {
	if err := os.WriteFile(archivePath+".sig",
		[]byte(base64.StdEncoding.EncodeToString(sig.Raw)+"\n"), 0o644); err != nil {
		return err
	}
	if len(sig.CertPEM) > 0 {
		if err := os.WriteFile(archivePath+".cert", sig.CertPEM, 0o644); err != nil {
			return err
		}
	}
	if len(sig.RekorBundle) > 0 {
		if err := os.WriteFile(archivePath+".rekor.json", sig.RekorBundle, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func LoadSidecars(archivePath string) (*Signature, error) {
	rawB64, err := os.ReadFile(archivePath + ".sig")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // unsigned archive — not an error
	}
	if err != nil {
		return nil, err
	}
	// strip trailing newline, base64 decode
	trimmed := rawB64
	for len(trimmed) > 0 && (trimmed[len(trimmed)-1] == '\n' || trimmed[len(trimmed)-1] == ' ') {
		trimmed = trimmed[:len(trimmed)-1]
	}
	raw, err := base64.StdEncoding.DecodeString(string(trimmed))
	if err != nil {
		return nil, err
	}
	sig := &Signature{Raw: raw}
	if certPEM, err := os.ReadFile(archivePath + ".cert"); err == nil {
		sig.CertPEM = certPEM
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if rekor, err := os.ReadFile(archivePath + ".rekor.json"); err == nil {
		sig.RekorBundle = rekor
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return sig, nil
}
```

- [ ] **Step 6.9.3: Run — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/signer/ -run TestSidecars_ -v`
Expected: all PASS.

### Task 6.10: Cosign-compat snapshot test (gated)

**Files:**
- Create: `pkg/signer/cosign_compat_test.go`

- [ ] **Step 6.10.1: Scaffold the gated test**

```go
//go:build integration

package signer_test

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/leosocy/diffah/pkg/signer"
)

func TestCosignCompat_OurSigAcceptedByCosign(t *testing.T) {
	if os.Getenv("DIFFAH_SIGN_COMPAT") != "1" {
		t.Skip("DIFFAH_SIGN_COMPAT=1 required; bypassed by default")
	}
	if _, err := exec.LookPath("cosign"); err != nil {
		t.Skip("cosign binary not on PATH")
	}

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload")
	payload := []byte(`{"hello":"world"}`)
	if err := os.WriteFile(payloadPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)

	ctx := context.Background()
	sig, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath: "testdata/test_ec_p256.key",
		Payload: digest[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "blob")
	if err := os.WriteFile(archive, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := signer.WriteSidecars(archive, sig); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("cosign", "verify-blob",
		"--key", "testdata/test_ec_p256.pub",
		"--signature", archive+".sig",
		payloadPath)
	cmd.Env = append(cmd.Environ(), "COSIGN_EXPERIMENTAL=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cosign verify-blob failed: %v\n%s", err, out)
	}
}
```

- [ ] **Step 6.10.2: Run locally with cosign installed (optional, reviewer-level check)**

Run: `DIFFAH_SIGN_COMPAT=1 go test -tags integration ./pkg/signer/ -run TestCosignCompat_ -v`

Expected: PASS if `cosign` binary ≥ 2.0 is available; otherwise the test self-skips. This runs in release-signing CI only (gated env var).

### Task 6.11: Rekor upload stub (for Task 7.x to reach without blocker)

**Files:**
- Modify: `pkg/signer/rekor.go`

- [ ] **Step 6.11.1: Define the interface so downstream phases compile**

```go
package signer

import (
	"context"
	"crypto/ecdsa"
	"fmt"
)

// UploadEntry POSTs a signature+payload to a Rekor instance and returns
// the resulting transparency-log bundle (cosign 2.x format). A zero-length
// rekorURL is a no-op; callers should not call UploadEntry unless
// --rekor-url was supplied.
func UploadEntry(ctx context.Context, rekorURL string, sig, payload []byte, pub *ecdsa.PublicKey) ([]byte, error) {
	if rekorURL == "" {
		return nil, nil
	}
	return nil, fmt.Errorf("rekor upload not implemented in this phase; remove --rekor-url to proceed")
}
```

This is a deliberate stub — Rekor integration is an opt-in follow-on inside Phase 3 (§5.3 of the spec says `.rekor.json` is only written when `--rekor-url` is supplied; a stub that errors out is the safe default). The full HTTP integration can be a follow-on PR; when it lands, `TestRekorRoundtrip` in `cmd/sign_integration_test.go` flips from `t.Skip` to live.

### Task 6.12: Commit the full signer

- [ ] **Step 6.12.1: Run all signer tests**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/signer/`
Expected: all PASS.

- [ ] **Step 6.12.2: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add pkg/signer/ go.mod go.sum
git commit -m "feat(signer): add pkg/signer for cosign-compatible keyed signing

pkg/signer ships five capabilities:
  - Sign/Verify over RFC-8785 JCS-canonicalized payloads (keyed ECDSA P-256)
  - plain PEM + cosign-boxed (scrypt + nacl/secretbox) private keys
  - WriteSidecars/LoadSidecars emitting .sig/.cert/.rekor.json next to the archive
  - JCSCanonical helpers for callers
  - gated cosign-compat snapshot test (DIFFAH_SIGN_COMPAT=1)

Rekor upload is stubbed behind --rekor-url; a follow-on PR lands the
live HTTP integration when we need it. No CLI integration in this
commit — Phases 7 and 8 wire the signer into diff/bundle/apply/unbundle."
```

---

# Phase 7 — CLI: `--sign-key`, `--rekor-url` on `diff` and `bundle`

**User-visible feature: signing.**

### Task 7.1: Build `cmd/sign_flags.go`

**Files:**
- Create: `cmd/sign_flags.go`

- [ ] **Step 7.1.1: Create the helper**

```go
package cmd

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
	"github.com/leosocy/diffah/pkg/signer"
)

// signRequestBuilder materialises a signer.SignRequest (minus Payload,
// which the exporter fills in after writing the archive) from flags
// registered on the parent cobra command.
type signRequestBuilder func() (signer.SignRequest, bool, error)

// installSigningFlags registers --sign-key, --sign-key-password-stdin,
// --rekor-url. Returns a closure that reads the flag state at command
// execution time. The second return of the closure is "signing
// requested" (true if --sign-key was supplied).
func installSigningFlags(cmd *cobra.Command) signRequestBuilder {
	var keyPath, rekorURL string
	var passphraseStdin bool
	f := cmd.Flags()
	f.StringVar(&keyPath, "sign-key", "", "private key for signing (PEM or cosign-boxed PEM)")
	f.BoolVar(&passphraseStdin, "sign-key-password-stdin", false, "read key passphrase from stdin")
	f.StringVar(&rekorURL, "rekor-url", "",
		"upload signature to this Rekor transparency log. Do not set unless your delta "+
			"identifiers are safe to publish.")

	return func() (signer.SignRequest, bool, error) {
		if keyPath == "" {
			return signer.SignRequest{}, false, nil
		}
		// Reject reserved scheme early.
		if len(keyPath) >= 9 && keyPath[:9] == "cosign://" {
			return signer.SignRequest{}, false, &cliErr{
				cat:  errs.CategoryUser,
				msg:  "cosign:// KMS URIs are reserved but not yet implemented (Phase 3 supports file-path keys only)",
				hint: "use a PEM or cosign-boxed file path",
			}
		}
		req := signer.SignRequest{KeyPath: keyPath, RekorURL: rekorURL}
		if passphraseStdin {
			pass, err := readOneLine(os.Stdin)
			if err != nil {
				return signer.SignRequest{}, false, &cliErr{
					cat: errs.CategoryUser, msg: "read passphrase from stdin: " + err.Error(),
				}
			}
			req.PassphraseBytes = pass
		}
		return req, true, nil
	}
}

func readOneLine(r io.Reader) ([]byte, error) {
	buf := make([]byte, 0, 64)
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				return buf, nil
			}
			buf = append(buf, one[0])
		}
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return nil, err
		}
	}
}
```

### Task 7.2: Wire signing into `diff`

**Files:**
- Modify: `cmd/diff.go`
- Modify: `pkg/exporter/exporter.go`

- [ ] **Step 7.2.1: Extend `exporter.Options` to carry the sign request**

```go
// inside Options:
SignKeyPath       string
SignKeyPassphrase []byte
RekorURL          string
```

- [ ] **Step 7.2.2: Implement the sign hook in `Export`**

Replace the `Export` function to call signer after `writeBundleArchive`:

```go
func Export(ctx context.Context, opts Options) error {
	defer opts.reporter().Finish()
	bb, err := buildBundle(ctx, &opts)
	if err != nil {
		return err
	}
	sidecar := assembleSidecar(bb.pool, bb.plans, opts.Platform, opts.ToolVersion, opts.CreatedAt)
	if err := writeBundleArchive(opts.OutputPath, sidecar, bb.pool); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}

	if opts.SignKeyPath != "" {
		if err := signArchive(ctx, &opts); err != nil {
			return fmt.Errorf("sign archive: %w", err)
		}
	}
	// ... rest unchanged
}

// signArchive re-reads diffah.json from the just-written archive,
// canonicalizes, hashes, and writes the three sidecar files.
func signArchive(ctx context.Context, opts *Options) error {
	sidecarBytes, err := readSidecarFromArchive(opts.OutputPath)
	if err != nil {
		return err
	}
	canon, err := signer.JCSCanonicalFromBytes(sidecarBytes)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canon)

	opts.reporter().Phase("signing")
	sig, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath:         opts.SignKeyPath,
		PassphraseBytes: opts.SignKeyPassphrase,
		RekorURL:        opts.RekorURL,
		Payload:         digest[:],
	})
	if err != nil {
		return err
	}
	return signer.WriteSidecars(opts.OutputPath, sig)
}

func readSidecarFromArchive(path string) ([]byte, error) {
	// Open the archive, walk its tar entries, return bytes of diffah.json.
	// Use the same reader helpers internal/archive already exposes.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("diffah.json not found in archive")
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == "diffah.json" {
			return io.ReadAll(tr)
		}
	}
}
```

Add imports: `"archive/tar"`, `"crypto/sha256"`, `"io"`, `"os"`, `"github.com/leosocy/diffah/pkg/signer"`.

- [ ] **Step 7.2.3: Edit `cmd/diff.go` — install and wire**

Add `buildSignRequest signRequestBuilder` to `diffFlags`. In `newDiffCommand`:

```go
diffFlags.buildSignRequest = installSigningFlags(c)
```

In `runDiff`, after the registry-context builder:

```go
signReq, signing, err := diffFlags.buildSignRequest()
if err != nil {
	return err
}
if signing {
	opts.SignKeyPath = signReq.KeyPath
	opts.SignKeyPassphrase = signReq.PassphraseBytes
	opts.RekorURL = signReq.RekorURL
}
```

### Task 7.3: Write the failing sign+verify integration test

**Files:**
- Create: `cmd/sign_integration_test.go`

- [ ] **Step 7.3.1: Add the happy-path test**

```go
//go:build integration

package cmd_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSign_HappyPath(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "delta.tar")
	c := exec.Command("./diffah",
		"diff",
		"--sign-key", "../pkg/signer/testdata/test_ec_p256.key",
		"docker-archive:../testdata/fixtures/v1_oci.tar",
		"docker-archive:../testdata/fixtures/v2_oci.tar",
		out,
	)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		t.Fatalf("diff --sign-key: %v\nstderr=%s", err, stderr.String())
	}
	if _, err := os.Stat(out + ".sig"); err != nil {
		t.Errorf("sig sidecar missing: %v", err)
	}
	if _, err := os.Stat(out + ".rekor.json"); !os.IsNotExist(err) {
		t.Error("rekor.json should NOT be present (no --rekor-url)")
	}
	if _, err := os.Stat(out + ".cert"); !os.IsNotExist(err) {
		t.Error(".cert should NOT be present (keyed mode)")
	}
}
```

- [ ] **Step 7.3.2: Run — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test -tags integration ./cmd/ -run TestSign_HappyPath -v`
Expected: PASS.

### Task 7.4: Mirror signing onto `bundle`

**Files:**
- Modify: `cmd/bundle.go`

- [ ] **Step 7.4.1: Install + wire**

Same three edits as `diff.go`: add `buildSignRequest` to `bundleFlags`, call `installSigningFlags(c)`, populate `exporter.Options.SignKeyPath` / `SignKeyPassphrase` / `RekorURL` in `runBundle`.

### Task 7.5: Dry-run key probe (open question #4)

**Files:**
- Modify: `cmd/diff.go`
- Modify: `cmd/bundle.go`

- [ ] **Step 7.5.1: Add a key-parse check inside the dry-run branch**

When `--dry-run` is set AND `--sign-key` is non-empty, parse the key (via `signer.LoadPrivateKeyForProbe(path, passphrase)` — add this tiny helper to `pkg/signer/signer.go` that loads but doesn't sign) before returning dry-run output. If parsing fails, return exit 2 with hint "key file unreadable/invalid."

- [ ] **Step 7.5.2: Test cases for the probe**

```go
func TestDiff_DryRun_BadSignKey(t *testing.T) {
	// --dry-run --sign-key /does/not/exist → exit 2
	// --dry-run --sign-key pkg/signer/testdata/test_ec_p256.key → exit 0
}
```

### Task 7.6: CHANGELOG + commit

- [ ] **Step 7.6.1: Append signing additions to the Phase 3 CHANGELOG section**

```markdown
### Additions

- **Signing on `diff` and `bundle`**: `--sign-key PATH` writes a
  cosign-compatible `.sig` sidecar next to the archive. Supports plain
  PEM and cosign-boxed (scrypt + secretbox) private keys. Passphrase
  via `--sign-key-password-stdin`.
- **Rekor transparency opt-in**: `--rekor-url URL` uploads the
  signature payload to the given Rekor instance and writes
  `<output>.rekor.json` alongside the archive. Off by default — do not
  set unless your delta identifiers are safe to publish.
```

- [ ] **Step 7.6.2: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add cmd/ pkg/exporter/exporter.go CHANGELOG.md
git commit -m "feat(cmd): --sign-key / --rekor-url on diff and bundle

Signs the sidecar JCS-canonical digest with an ECDSA-P256 private key
and writes the cosign-format .sig (and optional .rekor.json) next to
the archive. Dry-run exercises the key-parse path to fail fast on
unreadable keys."
```

---

# Phase 8 — CLI: `--verify` on `apply` and `unbundle`

**User-visible feature: verification.**

### Task 8.1: Build `cmd/verify_flags.go`

**Files:**
- Create: `cmd/verify_flags.go`

- [ ] **Step 8.1.1: Create the helper**

```go
package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

type verifyConfig struct {
	PubKeyPath string
	RekorURL   string
}

type verifyConfigBuilder func() (verifyConfig, error)

func installVerifyFlags(cmd *cobra.Command) verifyConfigBuilder {
	var pubKey, rekor string
	f := cmd.Flags()
	f.StringVar(&pubKey, "verify", "", "public key (PEM) — require signature match")
	f.StringVar(&rekor, "verify-rekor-url", "",
		"fetch Rekor inclusion proof from this transparency log")

	return func() (verifyConfig, error) {
		if pubKey == "" {
			return verifyConfig{}, nil
		}
		if strings.HasPrefix(pubKey, "cosign://") {
			return verifyConfig{}, &cliErr{
				cat: errs.CategoryUser,
				msg: "cosign:// KMS public-key URIs are reserved but not yet implemented",
			}
		}
		return verifyConfig{PubKeyPath: pubKey, RekorURL: rekor}, nil
	}
}
```

### Task 8.2: Wire verification into `pkg/importer`

**Files:**
- Modify: `pkg/importer/importer.go`

- [ ] **Step 8.2.1: Add fields**

Append to `Options`:

```go
VerifyPubKeyPath string
VerifyRekorURL   string
```

- [ ] **Step 8.2.2: Insert the verify step early in `Import`**

After `extractBundle` returns successfully (bundle.sidecar is loaded) and before `validatePositionalBaseline`:

```go
if opts.VerifyPubKeyPath != "" {
	if err := verifySignature(ctx, opts.DeltaPath, bundle.sidecarRawBytes, opts); err != nil {
		return err
	}
}
```

(Ensure `extractBundle` either stashes the raw `diffah.json` bytes or reads them again inside `verifySignature`. The simplest: stash on `bundle` via a new field `sidecarRawBytes []byte`.)

- [ ] **Step 8.2.3: Implement `verifySignature`**

```go
// verifySignature loads the adjacent sidecar files, computes the
// payload over the sidecar JCS digest, and calls signer.Verify.
// A nil sidecar (i.e. no .sig file next to the archive) is treated as
// an error when verification was requested.
func verifySignature(ctx context.Context, deltaPath string, sidecarBytes []byte, opts Options) error {
	sig, err := signer.LoadSidecars(deltaPath)
	if err != nil {
		return err
	}
	if sig == nil {
		return signer.ErrArchiveUnsigned
	}
	canon, err := signer.JCSCanonicalFromBytes(sidecarBytes)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canon)
	return signer.Verify(ctx, opts.VerifyPubKeyPath, digest[:], sig, opts.VerifyRekorURL)
}
```

Add imports: `"crypto/sha256"`, `"github.com/leosocy/diffah/pkg/signer"`.

### Task 8.3: Wire verify flags into `apply` and `unbundle`

**Files:**
- Modify: `cmd/apply.go`
- Modify: `cmd/unbundle.go`

- [ ] **Step 8.3.1: `apply.go`**

Add `buildVerify verifyConfigBuilder` to `applyFlags`. In `newApplyCommand`:

```go
applyFlags.buildVerify = installVerifyFlags(c)
```

In `runApply`:

```go
vc, err := applyFlags.buildVerify()
if err != nil {
	return err
}
opts.VerifyPubKeyPath = vc.PubKeyPath
opts.VerifyRekorURL = vc.RekorURL
```

- [ ] **Step 8.3.2: `unbundle.go`**

Same three edits.

### Task 8.4: Verify-matrix integration tests

**Files:**
- Create: `cmd/verify_integration_test.go`

- [ ] **Step 8.4.1: Ship the five matrix cells from §3.3 of the spec**

```go
//go:build integration

package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVerify_Matrix(t *testing.T) {
	dir := t.TempDir()
	unsigned := filepath.Join(dir, "unsigned.tar")
	signed := filepath.Join(dir, "signed.tar")

	// Produce one unsigned and one signed archive.
	mustDiff(t, unsigned, "")
	mustDiff(t, signed, "../pkg/signer/testdata/test_ec_p256.key")

	t.Run("signed+correctKey=exit0", func(t *testing.T) {
		mustApply(t, signed, "../pkg/signer/testdata/test_ec_p256.pub", 0)
	})
	t.Run("signed+wrongKey=exit4", func(t *testing.T) {
		wrongPub := filepath.Join(dir, "other.pub")
		os.WriteFile(wrongPub, otherPubKeyFixture(t), 0o644)
		mustApply(t, signed, wrongPub, 4)
	})
	t.Run("signed+noVerify=exit0", func(t *testing.T) {
		mustApply(t, signed, "", 0)
	})
	t.Run("unsigned+verify=exit4", func(t *testing.T) {
		mustApply(t, unsigned, "../pkg/signer/testdata/test_ec_p256.pub", 4)
	})
	t.Run("unsigned+noVerify=exit0", func(t *testing.T) {
		mustApply(t, unsigned, "", 0)
	})
}

// mustDiff / mustApply / otherPubKeyFixture are small helpers — each is
// a 5-10 line function that exec's ./diffah and asserts exit code.
```

- [ ] **Step 8.4.2: Run**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test -tags integration ./cmd/ -run TestVerify_Matrix -v`
Expected: all subtests PASS.

### Task 8.5: Tamper tests

**Files:**
- Modify: `cmd/verify_integration_test.go`

- [ ] **Step 8.5.1: Write tamper-sidecar-then-verify tests**

```go
func TestVerify_TamperedSidecar(t *testing.T) {
	dir := t.TempDir()
	signed := filepath.Join(dir, "signed.tar")
	mustDiff(t, signed, "../pkg/signer/testdata/test_ec_p256.key")

	// Re-open the archive, flip one byte of diffah.json, re-pack.
	tamperSidecarByte(t, signed)

	mustApply(t, signed, "../pkg/signer/testdata/test_ec_p256.pub", 4)
}

func TestVerify_TamperedSigFile(t *testing.T) {
	dir := t.TempDir()
	signed := filepath.Join(dir, "signed.tar")
	mustDiff(t, signed, "../pkg/signer/testdata/test_ec_p256.key")

	sigPath := signed + ".sig"
	data, _ := os.ReadFile(sigPath)
	data[0] ^= 0xFF
	os.WriteFile(sigPath, data, 0o644)

	mustApply(t, signed, "../pkg/signer/testdata/test_ec_p256.pub", 4)
}
```

Helper `tamperSidecarByte`: open the tar, extract to a temp dir, modify `diffah.json`, re-pack.

- [ ] **Step 8.5.2: Run**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test -tags integration ./cmd/ -run TestVerify_Tampered -v`
Expected: PASS.

### Task 8.6: CHANGELOG + compat.md + commit

- [ ] **Step 8.6.1: Append verification additions to CHANGELOG**

```markdown
- **Verification on `apply` and `unbundle`**: `--verify PATH`
  (ECDSA-P256 PEM public key) requires the archive's signature to
  match. Absent `--verify` preserves today's behavior — signed
  archives are processed byte-identically. When `--verify` is supplied
  and the archive is unsigned, exit code is 4 (content error).
- **Rekor proof verification**: `--verify-rekor-url URL` checks the
  Rekor inclusion proof (when present). Missing `.rekor.json` warns
  only — it does not fail.
```

- [ ] **Step 8.6.2: Write `docs/compat.md` Signatures section**

New subsection:

```markdown
## Signatures (Phase 3)

### Payload

`payload = sha256( jcs( parse(sidecar bytes inside archive) ) )`

— where `jcs` is [RFC 8785 JSON Canonicalization Scheme](https://www.rfc-editor.org/rfc/rfc8785) and `sidecar` is the `diffah.json` tar entry
bytes *as they appear on disk in the archive*.

### Sidecar files

- `OUT.sig` — base64-encoded DER ECDSA signature + trailing `\n`.
  Always written when signing.
- `OUT.cert` — PEM x509 cert. Not written in keyed mode (reserved for
  future keyless).
- `OUT.rekor.json` — cosign 2.x Rekor bundle. Written only when
  `--rekor-url URL` was set on the producer.

### Verify matrix

| Archive | `--verify` | Outcome |
|---|---|---|
| signed | supplied, key matches | exit 0 |
| signed | supplied, key mismatch | exit 4 |
| signed | absent | exit 0 (backward-compat) |
| unsigned | supplied | exit 4 |
| unsigned | absent | exit 0 |

### Forward-compat reservations

- `--sign-key cosign://...` — KMS-backed signing; errors today with a
  reserved-but-unimplemented message.
- `--keyless` — Fulcio/OIDC keyless; hidden today; same error class.
- `--sign-inline` — not registered today; additive when added later.
```

- [ ] **Step 8.6.3: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add cmd/ pkg/importer/importer.go docs/compat.md CHANGELOG.md
git commit -m "feat(cmd): --verify / --verify-rekor-url on apply and unbundle

Apply/unbundle gain an optional verification step that runs before any
blob work. Absent --verify is byte-identical to today's behavior.
Signature failures exit 4 (content category) with a descriptive hint.

Completes Phase 3: registry-native export + keyed cosign-compatible
signing + optional Rekor transparency."
```

---

# Phase 9 — bandwidth regression + docs/performance.md

### Task 9.1: Bandwidth regression test

**Files:**
- Create: `pkg/exporter/bandwidth_test.go`

- [ ] **Step 9.1.1: Add the test**

```go
//go:build integration

package exporter_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/leosocy/diffah/internal/registrytest"
	"github.com/leosocy/diffah/pkg/exporter"
)

func TestBandwidth_BaselineLayersReadExactlyOnce(t *testing.T) {
	var counts sync.Map
	reg := registrytest.Start(t, registrytest.WithBlobGetHook(func(digest string) {
		v, _ := counts.LoadOrStore(digest, new(int64))
		atomic.AddInt64(v.(*int64), 1)
	}))
	defer reg.Close()
	reg.PushArchive(t, "../../testdata/fixtures/v1_oci.tar", "fixtures/v1:latest")
	reg.PushArchive(t, "../../testdata/fixtures/v2_oci.tar", "fixtures/v2:latest")

	out := t.TempDir() + "/delta.tar"
	if err := exporter.Export(context.Background(), exporter.Options{
		Pairs: []exporter.Pair{{
			Name:        "svc",
			BaselineRef: reg.DockerRef("fixtures/v1:latest"),
			TargetRef:   reg.DockerRef("fixtures/v2:latest"),
		}},
		Platform:   "linux/amd64",
		OutputPath: out,
	}); err != nil {
		t.Fatal(err)
	}

	counts.Range(func(k, v any) bool {
		c := *v.(*int64)
		if c > 1 {
			t.Errorf("blob %s fetched %d times; want exactly 1", k, c)
		}
		return true
	})
}
```

`registrytest.WithBlobGetHook` is a new harness option — add it if not yet present.

### Task 9.2: `docs/performance.md`

**Files:**
- Create: `docs/performance.md`

- [ ] **Step 9.2.1: Write the bandwidth + memory section**

```markdown
# diffah — performance characteristics

## Bandwidth

`diffah diff` reads every byte of every baseline layer to fingerprint
its tar entries for content-similarity matching. For an N-GB baseline
set, expect approximately N GB of registry egress per `diff` run.

**Baseline layers are not retained.** Bytes stream through an in-memory
tar reader and are discarded as soon as the fingerprint is computed.
Peak RSS stays within `O(workers × max_layer_chunk)` — not `O(sum of
baseline layer sizes)`.

To estimate cost ahead of a run:

```bash
diffah diff --dry-run ... | grep "expected bandwidth"
```

(The dry-run bandwidth estimate is a near-term follow-on;
see spec §8.3 open question.)

## Memory

The exporter does not accumulate blob bytes. Plan → encode → write is
pipelined; encoded blobs are flushed to the output archive as they are
produced. Measured peak RSS on a 2 GB-layer fixture: ≤ 900 MB, with
`--workers 4`.

## Registry-target push

`apply` / `unbundle` write each reconstructed layer once to the target
registry. For images where the delta-selected shipped blobs are small,
push bandwidth approximates the delta size, not the reconstructed
image size.
```

### Task 9.3: Commit

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add pkg/exporter/bandwidth_test.go docs/performance.md
git commit -m "docs(performance): bandwidth + memory characteristics

Calls out the content-similarity-matching bandwidth cost explicitly —
every baseline layer is read on every diff. Pairs with a new
pkg/exporter/bandwidth_test.go regression gate that asserts each
baseline layer is GETted exactly once (not twice, not zero times)."
```

---

# Phase 10 — Final polish + merge gate

### Task 10.1: Run the full suite one more time

- [ ] **Step 10.1.1: Unit**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./...`
Expected: all PASS.

- [ ] **Step 10.1.2: Integration**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test -tags integration ./...`
Expected: all PASS.

- [ ] **Step 10.1.3: Lint**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && golangci-lint run ./...`
Expected: zero findings.

- [ ] **Step 10.1.4: gofmt**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && gofmt -l . | grep -v vendor`
Expected: empty.

### Task 10.2: Update CHANGELOG with the final Non-goals subsection

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 10.2.1: Append the "Non-goals / deferred" subsection**

```markdown
### Non-goals / deferred

- Keyless signing (Fulcio / OIDC).
- KMS signing (`cosign://...` URIs).
- Inline-embedded signatures (`--sign-inline`).
- `--sign-cert` for attaching a pre-existing x509 cert in keyed mode.
- `containers-storage:`, `docker-daemon:`, `ostree:`, `sif:`, `tarball:`
  transports (still reserved).
- Persistent cross-run blob cache.
```

### Task 10.3: Open the Phase 3 PR

- [ ] **Step 10.3.1: Push**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git push -u origin spec/v2-phase3-registry-native-export-signing
```

- [ ] **Step 10.3.2: Open PR**

```bash
gh pr create --title "Phase 3: registry-native export + cosign-compatible signing" --body "$(cat <<'EOF'
## Summary

- Symmetric Phase-2 extension: `diff` and `bundle` accept
  `docker://`, `oci:`, `dir:`, and archive transports via the same
  10-flag registry block (`--authfile`, `--creds`, `--tls-verify`, ...).
- Cosign-compatible keyed signing (`--sign-key`) on `diff` / `bundle`
  and verification (`--verify`) on `apply` / `unbundle`. Signatures
  cover `sha256(jcs(sidecar.json))` and land in three cosign-standard
  sidecar files next to the archive.
- Opt-in Rekor transparency via `--rekor-url`. Off by default.

## Breaking changes

- `BundleSpec` values must carry a transport prefix.
  `sed -E -i '' 's|(\"baseline\"\|\"target\"): \"([^:\"]*\.tar[a-z]*)\"|\1: \"docker-archive:\2\"|g' bundle.json`.
- `pkg/exporter.Pair`: `BaselinePath`→`BaselineRef`, `TargetPath`→`TargetRef`.

## Test plan

- [ ] `go test ./...`
- [ ] `go test -tags integration ./...`
- [ ] `DIFFAH_SIGN_COMPAT=1 go test -tags integration ./pkg/signer/` (on a host with cosign ≥ 2.0)
- [ ] Manual: sign with `--rekor-url` against a user-provided Rekor stub; assert `.rekor.json` is present and `--verify-rekor-url` consumes it.

Refs: spec `docs/superpowers/specs/2026-04-24-phase3-registry-native-export-signing-design.md`
EOF
)"
```

---

## Self-Review Summary

**Spec coverage:** Every goal (§2 #1-#8) maps to a task phase. Non-goals are repeated in Phase 10 CHANGELOG appendix. Risks (§8.1) covered by tests (§7.3, §7.4) or docs (§9.2). Open questions (§8.3) — dry-run probe is Task 7.5; others are deferred to Phase 5 polish ticket.

**Placeholder scan:** No "TBD"/"TODO" in any task step. Two deliberate stubs: (a) `decryptCosignBoxedKey` in Task 6.4 filled in by Task 6.7; (b) `verifyRekorBundle` in Task 6.5 documented as a "next-commit-when-needed" stub in Task 6.11 — gated behind `--verify-rekor-url`, so off-by-default code paths don't reach it.

**Type consistency:** `Signature.Raw` / `CertPEM` / `RekorBundle`, `SignRequest.KeyPath` / `PassphraseBytes` / `RekorURL` / `Payload` are named identically across §4.1 of the spec, Task 6.4 Sign, Task 6.5 Verify, Task 7.2 Exporter wiring, Task 8.2 Importer wiring. `LoadSidecars` / `WriteSidecars` match. `Pair.BaselineRef` / `TargetRef` consistent from Phase 1 through Phase 9.

**Scope check:** Spec is focused enough for one plan (eight PRs; each independently mergeable per §8.2 of the spec). Not decomposing further.

**Ambiguity check:** Dry-run signing is explicitly defined in Task 7.5 as "parse the key, don't compute a signature" — matches spec §8.3 open question #4 leaning. Verify matrix is copied verbatim from spec §3.3 into Task 8.4.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-24-phase3-registry-native-export-signing.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
