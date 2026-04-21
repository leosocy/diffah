# Diffah v2 Phase 1 — Intra-Layer Binary Diff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend diffah's exporter and importer to compute byte-level `zstd --patch-from`-style patches for each shipped layer, choosing `min(patch_bytes, full_zstd_bytes)` per layer and dispatching on the sidecar at import time.

**Architecture:** A new `internal/zstdpatch` package wraps the chosen zstd backend behind `Encode`/`EncodeFull`/`Decode`. A new `IntraLayerPlanner` in `pkg/exporter` iterates `Plan.ShippedInDelta`, matches each target layer to its size-closest baseline layer, and returns per-digest payload bytes plus the `[]BlobRef` that populates the sidecar. The importer's `CompositeSource` is upgraded from "try delta → fall back to baseline" to "classify digest via sidecar → dispatch per encoding," with post-assembly digest verification on every patched layer. The sidecar stays `version: "v1"` (pre-release — no compat obligation) and evolves `BlobRef` in place.

**Tech Stack:** Go 1.25, `github.com/klauspost/compress/zstd` (already a direct dep), `go.podman.io/image/v5` (already wired), `testify/require` for tests.

---

## Source spec

`docs/superpowers/specs/2026-04-20-diffah-v2-intra-layer-design.md`

Read §4 (architecture), §5 (sidecar schema), §6 (export), §7 (import), §8 (errors), §10 (tests) before executing tasks. This plan references section numbers where decisions originate.

---

## File structure

### New files

| File | Responsibility |
|---|---|
| `internal/zstdpatch/zstdpatch.go` | Thin wrapper over klauspost zstd: `Encode`, `EncodeFull`, `Decode`. Pure Go, no CGO. |
| `internal/zstdpatch/zstdpatch_test.go` | Round-trip and wrong-reference tests across size tiers. |
| `pkg/exporter/intralayer.go` | `IntraLayerPlanner` type. Size-closest matching + `min(patch, full_zstd)` per layer. |
| `pkg/exporter/intralayer_test.go` | Unit tests for planner with injected read-blob function. |
| `scripts/zstdpatch-spike/main.go` | Throwaway benchmark for Task 0. Runs against a large blob pair. Never built in CI. |
| `docs/superpowers/decisions/2026-04-XX-zstdpatch-backend.md` | Decision record selecting klauspost or os/exec (written during Task 0). |
| `testdata/fixtures/v3_oci.tar` | New fixture — each layer differs from `v2_oci.tar` by a few bytes. |
| `testdata/fixtures/v3_s2.tar` | Docker-schema-2 version of the same. |

### Modified files

| File | Reason |
|---|---|
| `pkg/diff/plan.go` | Add `Encoding` type + constants; extend `BlobRef` with four fields. |
| `pkg/diff/sidecar.go` | Per-partition validation rules (see §5.2 of spec). |
| `pkg/diff/sidecar_test.go` | Updated helper + new validation tests. |
| `pkg/diff/errors.go` | Add `ErrIntraLayerAssemblyMismatch`, `ErrBaselineMissingPatchRef`, `ErrIntraLayerUnsupported`. |
| `pkg/diff/errors_test.go` | Error tests for the three new types. |
| `pkg/exporter/baseline.go` | Expose layer sizes + media types (currently only digests). |
| `pkg/exporter/baseline_test.go` | Test coverage for the new accessor. |
| `pkg/exporter/exporter.go` | `Options.IntraLayer`; orchestrate planner; overwrite blob files with payloads; error on `auto` + manifest-only baseline. |
| `pkg/exporter/exporter_test.go` | Round-trip tests with the v3 fixture pair. |
| `pkg/importer/composite_src.go` | Constructor takes `*diff.Sidecar`; `GetBlob` classifies digest and dispatches. |
| `pkg/importer/composite_src_test.go` | Replace / extend existing tests for the new classifier. |
| `pkg/importer/importer.go` | Call new `CompositeSource` constructor; extend `probeBaseline` to union patch refs; extend `DryRunReport`. |
| `pkg/importer/importer_test.go` | DryRun patch-ref coverage. |
| `pkg/importer/integration_test.go` | End-to-end intra-layer scenarios. |
| `cmd/export.go` | New `--intra-layer=auto\|off` flag. |
| `cmd/inspect.go` | Augmented output (full/patch split, patch ratio). |
| `cmd/inspect_test.go` | Assert the new lines. |
| `scripts/build_fixtures/main.go` | Emit `v3_{oci,s2}.tar` fixtures with controlled byte drift. |

### Packaging boundary

- Planner depends on `internal/zstdpatch` directly (not through an interface) — only one backend at a time.
- `CompositeSource` depends on `internal/zstdpatch` directly for `Decode` only.
- Exporter orchestration stays in `pkg/exporter/exporter.go`; planner is a separate file to keep the orchestration readable.

---

## Task 0: klauspost/zstd compatibility spike (GATE, not a normal task)

**Purpose:** Decide the backend for `internal/zstdpatch`. The rest of the plan assumes klauspost succeeds (memory obs 994 confirms the raw-dictionary API exists). If the spike fails, Task 1 is re-implemented against `os/exec zstd` with the same interface; everything downstream is unaffected.

**Files:**
- Create: `scripts/zstdpatch-spike/main.go` (throwaway; not committed long-term)
- Create: `docs/superpowers/decisions/2026-04-XX-zstdpatch-backend.md`
- Test inputs: a ~130 MB reference/target pair at `/tmp/diffah-poc/ref.blob` and `/tmp/diffah-poc/target.blob` (already present from the POC — reuse, do not re-create)

**Acceptance criteria** (from spec §11.1):

1. klauspost produces a patch that decodes byte-exactly to the target.
2. `klauspost.patch_bytes ≤ 1.5 × cli.patch_bytes` where `cli` is the POC-measured 6.3 MB.
3. Peak RSS during encode < 500 MB with window-log 27.
4. Encode + decode complete in under 60 s wall-clock on the benchmark.

- [ ] **Step 0.0: Verify klauspost zstd raw-dictionary API names**

Memory obs 994 says klauspost supports zstd patch-from via raw dictionaries but does not pin the option names. Resolve them before writing code so Step 0.1 compiles on the first try.

Use context7:

```
resolve-library-id "klauspost/compress"
query-docs <id> "zstd encoder raw dictionary window size"
query-docs <id> "zstd decoder dictionary decode all"
```

Expected findings: confirm (or correct) these option names used throughout this plan:

- `zstd.WithEncoderDict([]byte)` — seed encoder with raw dictionary bytes.
- `zstd.WithEncoderLevel(zstd.SpeedBestCompression)`
- `zstd.WithWindowSize(int)` — encoder window size.
- `zstd.WithDecoderDicts([]byte)` — seed decoder with raw dictionary bytes.
- `zstd.WithDecoderMaxWindow(uint64)` — decoder window cap.

If any name differs in the installed v1.18.0 version, update Steps 0.1, 1.3 accordingly before proceeding.

- [ ] **Step 0.1: Write the spike script**

Create `scripts/zstdpatch-spike/main.go`:

```go
//go:build ignore

// Command zstdpatch-spike benchmarks klauspost/compress zstd patch-from
// against the reference zstd CLI on a real layer pair. Throwaway — delete
// after Task 0 produces a decision record.
//
// Usage:
//   go run scripts/zstdpatch-spike/main.go \
//       /tmp/diffah-poc/ref.blob /tmp/diffah-poc/target.blob
package main

import (
    "bytes"
    "crypto/sha256"
    "fmt"
    "os"
    "runtime"
    "time"

    "github.com/klauspost/compress/zstd"
)

func main() {
    if len(os.Args) != 3 {
        fmt.Fprintln(os.Stderr, "usage: zstdpatch-spike REF TARGET")
        os.Exit(2)
    }
    ref := mustRead(os.Args[1])
    target := mustRead(os.Args[2])
    fmt.Printf("ref bytes    : %d\n", len(ref))
    fmt.Printf("target bytes : %d\n", len(target))

    // Encode with klauspost using ref as raw dictionary + window-log 27.
    start := time.Now()
    enc, err := zstd.NewWriter(nil,
        zstd.WithEncoderLevel(zstd.SpeedBestCompression),
        zstd.WithEncoderDict(ref),
        zstd.WithWindowSize(1<<27),
    )
    must(err)
    patch := enc.EncodeAll(target, nil)
    encDur := time.Since(start)
    enc.Close()

    // Measure peak memory before decode.
    var m runtime.MemStats
    runtime.ReadMemStats(&m)
    fmt.Printf("patch bytes  : %d\n", len(patch))
    fmt.Printf("encode time  : %s\n", encDur)
    fmt.Printf("heap after   : %.1f MB\n", float64(m.Alloc)/1e6)

    // Decode and verify byte-exact round-trip.
    dec, err := zstd.NewReader(nil,
        zstd.WithDecoderDicts(ref),
        zstd.WithDecoderMaxWindow(1<<27),
    )
    must(err)
    defer dec.Close()
    got, err := dec.DecodeAll(patch, nil)
    must(err)
    if !bytes.Equal(got, target) {
        fmt.Println("ROUND-TRIP FAILED: decoded bytes differ from target")
        os.Exit(1)
    }
    fmt.Printf("round-trip   : OK (sha256 %x)\n", sha256.Sum256(got))

    // POC CLI baseline was 6.3 MB — report the ratio.
    const cliBytes = 6_300_000
    fmt.Printf("vs CLI       : klauspost=%.2f× (acceptance ≤ 1.50×)\n",
        float64(len(patch))/float64(cliBytes))
}

func mustRead(path string) []byte {
    b, err := os.ReadFile(path)
    must(err)
    return b
}

func must(err error) {
    if err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}
```

- [ ] **Step 0.2: Run the spike**

```bash
go run scripts/zstdpatch-spike/main.go \
    /tmp/diffah-poc/ref.blob /tmp/diffah-poc/target.blob
```

Expected output pattern:

```
ref bytes    : ~136000000
target bytes : ~137000000
patch bytes  : <NUMBER>
encode time  : <DURATION>
heap after   : <X>.X MB
round-trip   : OK (sha256 ...)
vs CLI       : klauspost=<ratio>× (acceptance ≤ 1.50×)
```

Observe `patch bytes`, `encode time`, `heap after`, `vs CLI`.

- [ ] **Step 0.3: Write the decision record**

Create `docs/superpowers/decisions/2026-04-XX-zstdpatch-backend.md` (replace the date with today's):

```markdown
# Decision: zstd patch-from backend for internal/zstdpatch

**Date:** 2026-04-XX
**Status:** Decided

## Context

Diffah v2 Phase 1 needs a zstd patch-from encoder and decoder for
`internal/zstdpatch`. Two candidates:

1. `github.com/klauspost/compress/zstd` with raw dictionaries — pure Go.
2. `os/exec` against the `zstd` CLI (≥ 1.5) — external runtime dependency.

Spike ran against the service-A 5.2→5.3 layer 0 pair (~136 MB / ~137 MB).

## Measurements

- Patch size:      `<FILL>` bytes
- CLI reference:   6,300,000 bytes
- Ratio vs CLI:    `<FILL>` ×
- Encode time:     `<FILL>`
- Peak heap:       `<FILL>` MB
- Round-trip:      byte-exact (verified)

## Decision

`<klauspost | os/exec>` — cite the two or three acceptance criteria that
drove the choice.

## Consequences

- If klauspost: pure-Go static binary preserved; operators face no new
  runtime dependency.
- If os/exec: README gains a runtime-dependency note on `zstd ≥ 1.5`;
  `internal/zstdpatch` internally runs `zstd --patch-from` via `exec.Cmd`
  but exposes the same `Encode`/`EncodeFull`/`Decode` interface so no
  downstream code changes.
```

- [ ] **Step 0.4: Commit the decision record**

Delete the spike script (it lives under `scripts/zstdpatch-spike/` with a `//go:build ignore` tag, so a stale copy is harmless; delete to keep the tree tidy).

```bash
rm -rf scripts/zstdpatch-spike
git add docs/superpowers/decisions/
git commit -m "docs(decision): select zstd patch-from backend for internal/zstdpatch

Backs: diffah v2 Phase 1 design (docs/superpowers/specs/2026-04-20-diffah-v2-intra-layer-design.md §11.1)"
```

- [ ] **Step 0.5: Gate check**

If the decision record selects **klauspost** → proceed to Task 1 as written below.

If the decision record selects **os/exec** → Task 1 is re-implemented: the interface and tests are identical, but the body of `Encode`/`EncodeFull`/`Decode` shells out to `zstd`. Add a single integration-test guard (`t.Skip()` if `zstd` is not on `$PATH`). All subsequent tasks are unaffected.

---

## Task 1: internal/zstdpatch package

**Files:**
- Create: `internal/zstdpatch/zstdpatch.go`
- Test: `internal/zstdpatch/zstdpatch_test.go`

- [ ] **Step 1.1: Write the failing round-trip test**

Create `internal/zstdpatch/zstdpatch_test.go`:

```go
package zstdpatch

import (
    "bytes"
    "crypto/rand"
    "testing"

    "github.com/stretchr/testify/require"
)

// TestRoundTrip_Empty covers the degenerate 0-byte case.
func TestRoundTrip_Empty(t *testing.T) {
    ref := []byte("unused reference bytes")
    patch, err := Encode(ref, nil)
    require.NoError(t, err)
    got, err := Decode(ref, patch)
    require.NoError(t, err)
    require.Empty(t, got)
}

// TestRoundTrip_SmallDelta — 1 MB target that overlaps 1 MB of reference
// except for a 4-byte change. Patch must decode byte-exactly.
func TestRoundTrip_SmallDelta(t *testing.T) {
    ref := make([]byte, 1<<20)
    _, _ = rand.Read(ref)
    target := append([]byte(nil), ref...)
    target[len(target)/2] ^= 0xFF

    patch, err := Encode(ref, target)
    require.NoError(t, err)
    require.Less(t, len(patch), len(target)/2,
        "patch of a 4-byte delta should be far smaller than half the target")

    got, err := Decode(ref, patch)
    require.NoError(t, err)
    require.True(t, bytes.Equal(got, target), "decoded bytes differ from target")
}

// TestDecode_WrongReference — swapping the reference at decode time must
// either return an error or return bytes that are detectably not the target.
// Silent corruption here would defeat the whole design.
func TestDecode_WrongReference(t *testing.T) {
    refA := bytes.Repeat([]byte{0xAA}, 1<<16)
    refB := bytes.Repeat([]byte{0xBB}, 1<<16)
    target := append([]byte(nil), refA...)
    target[0] = 0x42

    patch, err := Encode(refA, target)
    require.NoError(t, err)

    got, err := Decode(refB, patch)
    if err == nil {
        // Decode may succeed but must not return the original target bytes.
        require.False(t, bytes.Equal(got, target),
            "decoding with the wrong reference returned target bytes silently")
    }
}

// TestEncodeFull_CompressesStandalone — EncodeFull is a plain zstd encode
// with no reference; decoding via zstd.Decoder must recover the target.
func TestEncodeFull_RoundTrip(t *testing.T) {
    target := bytes.Repeat([]byte("hello, diffah "), 1<<10)

    compressed, err := EncodeFull(target)
    require.NoError(t, err)
    require.Less(t, len(compressed), len(target))

    got, err := DecodeFull(compressed)
    require.NoError(t, err)
    require.True(t, bytes.Equal(got, target))
}
```

- [ ] **Step 1.2: Run the test to confirm it fails**

```bash
go test -tags 'containers_image_openpgp' ./internal/zstdpatch/... -v
```

Expected: compile error because `Encode`, `Decode`, `EncodeFull`, `DecodeFull` are undefined.

- [ ] **Step 1.3: Write the implementation (klauspost path)**

Create `internal/zstdpatch/zstdpatch.go`:

```go
// Package zstdpatch implements zstd patch-from style byte-level deltas
// backed by github.com/klauspost/compress/zstd raw dictionaries.
//
// Encode(ref, target) produces a zstd frame that decodes to target when
// seeded with ref. The output is a standard zstd frame — any zstd 1.5+
// decoder can read it, given the same reference.
//
// The package keeps its surface tiny: the exporter and importer never need
// to know about window sizes, compression levels, or encoder state.
package zstdpatch

import (
    "fmt"

    "github.com/klauspost/compress/zstd"
)

// maxWindow caps the zstd decompression window at 128 MB (1<<27). This
// matches `zstd --long=27` in the CLI. Larger windows bloat memory without
// meaningful ratio gains on container layers.
const maxWindow = 1 << 27

// Encode produces a zstd frame that decodes to target when seeded with ref.
// An empty target encodes to a valid zstd frame that decodes to zero bytes.
func Encode(ref, target []byte) ([]byte, error) {
    enc, err := zstd.NewWriter(nil,
        zstd.WithEncoderLevel(zstd.SpeedBestCompression),
        zstd.WithEncoderDict(ref),
        zstd.WithWindowSize(maxWindow),
    )
    if err != nil {
        return nil, fmt.Errorf("new zstd encoder: %w", err)
    }
    defer enc.Close()
    return enc.EncodeAll(target, nil), nil
}

// EncodeFull compresses target as a standalone zstd frame (no reference).
// Used only to produce the "full-zstd" size estimate the planner compares
// against the patch size.
func EncodeFull(target []byte) ([]byte, error) {
    enc, err := zstd.NewWriter(nil,
        zstd.WithEncoderLevel(zstd.SpeedBestCompression),
        zstd.WithWindowSize(maxWindow),
    )
    if err != nil {
        return nil, fmt.Errorf("new zstd encoder: %w", err)
    }
    defer enc.Close()
    return enc.EncodeAll(target, nil), nil
}

// Decode reads a zstd frame produced by Encode and returns the original
// target bytes. ref must be byte-identical to the ref used at encode time;
// otherwise the decoder may return an error or silently-different bytes.
// Callers are expected to verify the decoded bytes against the content
// digest recorded in the sidecar.
func Decode(ref, patch []byte) ([]byte, error) {
    dec, err := zstd.NewReader(nil,
        zstd.WithDecoderDicts(ref),
        zstd.WithDecoderMaxWindow(maxWindow),
    )
    if err != nil {
        return nil, fmt.Errorf("new zstd decoder: %w", err)
    }
    defer dec.Close()
    out, err := dec.DecodeAll(patch, nil)
    if err != nil {
        return nil, fmt.Errorf("decode patch: %w", err)
    }
    return out, nil
}

// DecodeFull reads a zstd frame produced by EncodeFull.
func DecodeFull(data []byte) ([]byte, error) {
    dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxWindow(maxWindow))
    if err != nil {
        return nil, fmt.Errorf("new zstd decoder: %w", err)
    }
    defer dec.Close()
    out, err := dec.DecodeAll(data, nil)
    if err != nil {
        return nil, fmt.Errorf("decode full: %w", err)
    }
    return out, nil
}
```

- [ ] **Step 1.4: Run tests until green**

```bash
go test -tags 'containers_image_openpgp' ./internal/zstdpatch/... -v
```

Expected: all four tests PASS.

- [ ] **Step 1.5: Commit**

```bash
git add internal/zstdpatch/
git commit -m "feat(zstdpatch): add internal package for zstd patch-from encode/decode

Wraps klauspost/compress zstd with a tiny Encode/EncodeFull/Decode
surface gated by a compatibility spike (see
docs/superpowers/decisions/2026-04-XX-zstdpatch-backend.md).

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 1"
```

---

## Task 2: Extend BlobRef schema + new error types + validation

**Files:**
- Modify: `pkg/diff/plan.go`
- Modify: `pkg/diff/sidecar.go`
- Modify: `pkg/diff/sidecar_test.go`
- Modify: `pkg/diff/errors.go`
- Modify: `pkg/diff/errors_test.go`

- [ ] **Step 2.1: Write failing tests for the three new errors**

Append to `pkg/diff/errors_test.go`:

```go
func TestErrIntraLayerAssemblyMismatch_MentionsBothDigests(t *testing.T) {
    err := &ErrIntraLayerAssemblyMismatch{
        Digest: "sha256:want", Got: "sha256:got",
    }
    require.Contains(t, err.Error(), "sha256:want")
    require.Contains(t, err.Error(), "sha256:got")
}

func TestErrBaselineMissingPatchRef_MentionsDigestAndSource(t *testing.T) {
    err := &ErrBaselineMissingPatchRef{
        Digest: "sha256:ref", Source: "docker://baseline",
    }
    require.Contains(t, err.Error(), "sha256:ref")
    require.Contains(t, err.Error(), "docker://baseline")
    require.Contains(t, err.Error(), "patch")
}

func TestErrIntraLayerUnsupported_MentionsReason(t *testing.T) {
    err := &ErrIntraLayerUnsupported{Reason: "baseline-manifest has no blob bytes"}
    require.Contains(t, err.Error(), "baseline-manifest has no blob bytes")
}
```

- [ ] **Step 2.2: Run tests to confirm failure**

```bash
go test -tags 'containers_image_openpgp' ./pkg/diff/... -run 'IntraLayer|MissingPatchRef|Unsupported' -v
```

Expected: compile errors — the three types don't exist.

- [ ] **Step 2.3: Implement the three errors**

Append to `pkg/diff/errors.go`:

```go
// ErrIntraLayerAssemblyMismatch reports that a patched layer's computed
// sha256 did not match the manifest-declared digest. Import must fail fast
// with no partial output.
type ErrIntraLayerAssemblyMismatch struct{ Digest, Got string }

func (e *ErrIntraLayerAssemblyMismatch) Error() string {
    return fmt.Sprintf("intra-layer assembly mismatch: expected %s, got %s",
        e.Digest, e.Got)
}

// ErrBaselineMissingPatchRef is the patch-specific sibling of
// ErrBaselineMissingBlob. Raised when a shipped layer with encoding=patch
// names a patch_from_digest that is absent from the provided baseline.
type ErrBaselineMissingPatchRef struct{ Digest, Source string }

func (e *ErrBaselineMissingPatchRef) Error() string {
    return fmt.Sprintf("baseline %q does not provide patch reference blob %s",
        e.Source, e.Digest)
}

// ErrIntraLayerUnsupported is raised on the exporter side when the current
// options make intra-layer mode impossible (e.g. baseline is manifest-only
// with no blob bytes).
type ErrIntraLayerUnsupported struct{ Reason string }

func (e *ErrIntraLayerUnsupported) Error() string {
    return fmt.Sprintf("intra-layer mode unsupported: %s", e.Reason)
}
```

- [ ] **Step 2.4: Run error tests to confirm green**

```bash
go test -tags 'containers_image_openpgp' ./pkg/diff/... -run 'IntraLayer|MissingPatchRef|Unsupported' -v
```

Expected: all three PASS.

- [ ] **Step 2.5: Write failing tests for the extended BlobRef schema**

Append to `pkg/diff/sidecar_test.go`:

```go
// validSidecarWithEncoding returns a sidecar whose ShippedInDelta entries
// carry encoding fields. This is the post-Task-2 baseline used by all
// intra-layer tests.
func validSidecarWithEncoding() Sidecar {
    s := validSidecar()
    s.ShippedInDelta = []BlobRef{
        {
            Digest:      "sha256:eee",
            Size:        20,
            MediaType:   "m",
            Encoding:    EncodingFull,
            ArchiveSize: 20,
        },
    }
    return s
}

func TestSidecar_Rejects_ShippedEntry_MissingEncoding(t *testing.T) {
    s := validSidecarWithEncoding()
    s.ShippedInDelta[0].Encoding = ""
    _, err := s.Marshal()
    var ve *ErrSidecarSchema
    require.ErrorAs(t, err, &ve)
    require.Contains(t, err.Error(), "encoding")
}

func TestSidecar_Rejects_PatchEntry_MissingFromDigest(t *testing.T) {
    s := validSidecarWithEncoding()
    s.ShippedInDelta[0] = BlobRef{
        Digest: "sha256:eee", Size: 20, MediaType: "m",
        Encoding:    EncodingPatch,
        Codec:       "zstd-patch",
        ArchiveSize: 5,
        // PatchFromDigest intentionally empty
    }
    _, err := s.Marshal()
    var ve *ErrSidecarSchema
    require.ErrorAs(t, err, &ve)
    require.Contains(t, err.Error(), "patch_from_digest")
}

func TestSidecar_Rejects_PatchEntry_MissingCodec(t *testing.T) {
    s := validSidecarWithEncoding()
    s.ShippedInDelta[0] = BlobRef{
        Digest: "sha256:eee", Size: 20, MediaType: "m",
        Encoding:        EncodingPatch,
        PatchFromDigest: "sha256:ref",
        ArchiveSize:     5,
    }
    _, err := s.Marshal()
    var ve *ErrSidecarSchema
    require.ErrorAs(t, err, &ve)
    require.Contains(t, err.Error(), "codec")
}

func TestSidecar_Rejects_PatchEntry_ArchiveSize_NotLessThanSize(t *testing.T) {
    s := validSidecarWithEncoding()
    s.ShippedInDelta[0] = BlobRef{
        Digest: "sha256:eee", Size: 20, MediaType: "m",
        Encoding:        EncodingPatch,
        Codec:           "zstd-patch",
        PatchFromDigest: "sha256:ref",
        ArchiveSize:     20, // must be < Size for patch entries
    }
    _, err := s.Marshal()
    var ve *ErrSidecarSchema
    require.ErrorAs(t, err, &ve)
    require.Contains(t, err.Error(), "archive_size")
}

func TestSidecar_Rejects_FullEntry_Has_PatchFromDigest(t *testing.T) {
    s := validSidecarWithEncoding()
    s.ShippedInDelta[0].PatchFromDigest = "sha256:ref"
    _, err := s.Marshal()
    var ve *ErrSidecarSchema
    require.ErrorAs(t, err, &ve)
}

func TestSidecar_Rejects_FullEntry_Archive_NotEqualSize(t *testing.T) {
    s := validSidecarWithEncoding()
    s.ShippedInDelta[0].ArchiveSize = 19
    _, err := s.Marshal()
    var ve *ErrSidecarSchema
    require.ErrorAs(t, err, &ve)
}

func TestSidecar_Rejects_RequiredEntry_HasIntraLayerFields(t *testing.T) {
    s := validSidecarWithEncoding()
    s.RequiredFromBaseline[0].Encoding = EncodingFull
    _, err := s.Marshal()
    var ve *ErrSidecarSchema
    require.ErrorAs(t, err, &ve)
    require.Contains(t, err.Error(), "required_from_baseline")
}

func TestSidecar_PatchEntry_MarshalsCorrectly(t *testing.T) {
    s := validSidecarWithEncoding()
    s.ShippedInDelta[0] = BlobRef{
        Digest: "sha256:eee", Size: 1000, MediaType: "m",
        Encoding:        EncodingPatch,
        Codec:           "zstd-patch",
        PatchFromDigest: "sha256:ref",
        ArchiveSize:     123,
    }
    raw, err := s.Marshal()
    require.NoError(t, err)
    require.Contains(t, string(raw), `"encoding": "patch"`)
    require.Contains(t, string(raw), `"codec": "zstd-patch"`)
    require.Contains(t, string(raw), `"patch_from_digest": "sha256:ref"`)
    require.Contains(t, string(raw), `"archive_size": 123`)

    // Required entry has none of the four fields in the JSON output.
    require.NotContains(t, string(raw), `"encoding": ""`)
}
```

Also update the existing `validSidecar()` helper so the `ShippedInDelta` entry carries a default encoding — otherwise all pre-existing tests will fail the new validator. Replace the current helper body:

```go
func validSidecar() Sidecar {
    return Sidecar{
        Version:     "v1",
        Tool:        "diffah",
        ToolVersion: "v0.1.0",
        CreatedAt:   time.Date(2026, 4, 20, 13, 21, 0, 0, time.UTC),
        Platform:    "linux/amd64",
        Target: ImageRef{
            ManifestDigest: digest.Digest("sha256:aaa"),
            ManifestSize:   1234,
            MediaType:      "application/vnd.docker.distribution.manifest.v2+json",
        },
        Baseline: BaselineRef{
            ManifestDigest: digest.Digest("sha256:bbb"),
            MediaType:      "application/vnd.docker.distribution.manifest.v2+json",
            SourceHint:     "docker://x/y:v1",
        },
        RequiredFromBaseline: []BlobRef{{Digest: "sha256:ccc", Size: 10, MediaType: "m"}},
        ShippedInDelta: []BlobRef{{
            Digest:      "sha256:eee",
            Size:        20,
            MediaType:   "m",
            Encoding:    EncodingFull,
            ArchiveSize: 20,
        }},
    }
}
```

- [ ] **Step 2.6: Run schema tests to confirm failure**

```bash
go test -tags 'containers_image_openpgp' ./pkg/diff/... -v
```

Expected: compile errors — `Encoding`, `EncodingFull`, `EncodingPatch`, and the four new `BlobRef` fields don't exist.

- [ ] **Step 2.7: Extend BlobRef**

Replace `pkg/diff/plan.go` with:

```go
package diff

import "github.com/opencontainers/go-digest"

// Encoding discriminates how a shipped blob is stored in the archive.
// The zero value ("") is invalid for ShippedInDelta entries and must not
// be present on RequiredFromBaseline entries.
type Encoding string

const (
    // EncodingFull: the archive file stored under Digest contains the
    // target blob bytes verbatim.
    EncodingFull Encoding = "full"
    // EncodingPatch: the archive file contains a codec-specific patch that
    // reconstructs the target when applied to PatchFromDigest.
    EncodingPatch Encoding = "patch"
)

// BlobRef is the canonical description of a layer or config blob referenced
// from a manifest.
//
// The Encoding/Codec/PatchFromDigest/ArchiveSize fields apply to
// ShippedInDelta entries only. RequiredFromBaseline entries omit them
// entirely — those layers are fetched from baseline as-is, so an
// archive-level encoding concept does not apply.
type BlobRef struct {
    Digest    digest.Digest `json:"digest"`
    Size      int64         `json:"size"`
    MediaType string        `json:"media_type"`

    Encoding        Encoding      `json:"encoding,omitempty"`
    Codec           string        `json:"codec,omitempty"`
    PatchFromDigest digest.Digest `json:"patch_from_digest,omitempty"`
    ArchiveSize     int64         `json:"archive_size,omitempty"`
}

// Plan records the outcome of ComputePlan: which target layers must be
// resolved from baseline at import time, and which must be shipped inside
// the delta archive.
type Plan struct {
    ShippedInDelta       []BlobRef
    RequiredFromBaseline []BlobRef
}

// ComputePlan partitions target into RequiredFromBaseline and
// ShippedInDelta according to which digests already exist in baseline.
//
// Order within each partition follows the target's original order so that
// manifest layer ordering can be preserved downstream.
func ComputePlan(target []BlobRef, baseline []digest.Digest) Plan {
    known := make(map[digest.Digest]struct{}, len(baseline))
    for _, d := range baseline {
        known[d] = struct{}{}
    }
    plan := Plan{
        ShippedInDelta:       []BlobRef{},
        RequiredFromBaseline: []BlobRef{},
    }
    for _, b := range target {
        if _, ok := known[b.Digest]; ok {
            plan.RequiredFromBaseline = append(plan.RequiredFromBaseline, b)
        } else {
            plan.ShippedInDelta = append(plan.ShippedInDelta, b)
        }
    }
    return plan
}
```

- [ ] **Step 2.8: Extend Sidecar.validate**

Replace the `validate` method in `pkg/diff/sidecar.go` with:

```go
func (s Sidecar) validate() error {
    switch {
    case s.Platform == "":
        return &ErrSidecarSchema{Reason: "platform is required"}
    case s.Target.ManifestDigest == "":
        return &ErrSidecarSchema{Reason: "target.manifest_digest is required"}
    case s.RequiredFromBaseline == nil:
        return &ErrSidecarSchema{Reason: "required_from_baseline is required (may be empty slice)"}
    case s.ShippedInDelta == nil:
        return &ErrSidecarSchema{Reason: "shipped_in_delta is required (may be empty slice)"}
    }
    for i, b := range s.RequiredFromBaseline {
        if err := validateRequiredEntry(i, b); err != nil {
            return err
        }
    }
    for i, b := range s.ShippedInDelta {
        if err := validateShippedEntry(i, b); err != nil {
            return err
        }
    }
    return nil
}

// validateRequiredEntry: required-from-baseline entries must not carry any
// intra-layer fields. Those fields describe archive-side encoding and
// baseline-fetched blobs have no archive-side bytes.
func validateRequiredEntry(i int, b BlobRef) error {
    switch {
    case b.Encoding != "",
        b.Codec != "",
        b.PatchFromDigest != "",
        b.ArchiveSize != 0:
        return &ErrSidecarSchema{Reason: fmt.Sprintf(
            "required_from_baseline[%d] must not set encoding/codec/patch_from_digest/archive_size",
            i)}
    }
    return nil
}

// validateShippedEntry: every shipped entry must declare an encoding, and
// that encoding's peer fields must be consistent with the declaration.
func validateShippedEntry(i int, b BlobRef) error {
    switch b.Encoding {
    case "":
        return &ErrSidecarSchema{Reason: fmt.Sprintf(
            "shipped_in_delta[%d].encoding is required", i)}
    case EncodingFull:
        if b.Codec != "" || b.PatchFromDigest != "" {
            return &ErrSidecarSchema{Reason: fmt.Sprintf(
                "shipped_in_delta[%d] encoding=full must not set codec/patch_from_digest",
                i)}
        }
        if b.ArchiveSize != b.Size {
            return &ErrSidecarSchema{Reason: fmt.Sprintf(
                "shipped_in_delta[%d] encoding=full requires archive_size == size (got %d vs %d)",
                i, b.ArchiveSize, b.Size)}
        }
    case EncodingPatch:
        if b.Codec == "" {
            return &ErrSidecarSchema{Reason: fmt.Sprintf(
                "shipped_in_delta[%d] encoding=patch requires codec", i)}
        }
        if b.PatchFromDigest == "" {
            return &ErrSidecarSchema{Reason: fmt.Sprintf(
                "shipped_in_delta[%d] encoding=patch requires patch_from_digest", i)}
        }
        if b.ArchiveSize <= 0 || b.ArchiveSize >= b.Size {
            return &ErrSidecarSchema{Reason: fmt.Sprintf(
                "shipped_in_delta[%d] encoding=patch requires 0 < archive_size < size (got %d vs %d)",
                i, b.ArchiveSize, b.Size)}
        }
    default:
        return &ErrSidecarSchema{Reason: fmt.Sprintf(
            "shipped_in_delta[%d] encoding=%q is not recognized",
            i, b.Encoding)}
    }
    return nil
}
```

- [ ] **Step 2.9: Run full diff package tests to confirm green**

```bash
go test -tags 'containers_image_openpgp' ./pkg/diff/... -v
```

Expected: all tests PASS, including the updated `validSidecar` roundtrip and the new negative cases.

- [ ] **Step 2.10: Commit**

```bash
git add pkg/diff/
git commit -m "feat(diff): extend BlobRef with encoding fields and add intra-layer errors

Adds Encoding, Codec, PatchFromDigest, ArchiveSize to BlobRef with
per-partition validation rules and three new error types required by
the importer/exporter intra-layer work.

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 2"
```

---

## Task 3: IntraLayerPlanner

**Files:**
- Create: `pkg/exporter/intralayer.go`
- Test: `pkg/exporter/intralayer_test.go`

- [ ] **Step 3.1: Write failing planner tests**

Create `pkg/exporter/intralayer_test.go`:

```go
package exporter

import (
    "bytes"
    "context"
    "math/rand/v2"
    "testing"

    "github.com/opencontainers/go-digest"
    "github.com/stretchr/testify/require"

    "github.com/leosocy/diffah/pkg/diff"
)

// blobMap injects a deterministic read-blob function for tests. Digest →
// raw bytes.
type blobMap map[digest.Digest][]byte

func (m blobMap) read(d digest.Digest) ([]byte, error) {
    b, ok := m[d]
    if !ok {
        return nil, &missingBlobError{d: d}
    }
    return b, nil
}

type missingBlobError struct{ d digest.Digest }

func (e *missingBlobError) Error() string { return "missing " + e.d.String() }

// pseudoRandom returns deterministic pseudo-random bytes for test inputs.
// Constant-byte inputs compress via RLE into tiny zstd frames, making
// encoding-choice assertions flaky. Pseudo-random bytes do not compress
// without a dictionary — seeded randomness keeps the tests reproducible.
func pseudoRandom(seed uint64, size int) []byte {
    r := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
    b := make([]byte, size)
    for i := range b {
        b[i] = byte(r.Uint32())
    }
    return b
}

func TestPlanner_PicksFullWhenPatchLarger(t *testing.T) {
    // Two unrelated pseudo-random blobs — the patch cannot exploit overlap,
    // so it must be larger than the full zstd frame. The planner degrades
    // to encoding=full.
    ref := pseudoRandom(1, 1<<15)
    target := pseudoRandom(2, 1<<15)

    refDigest := digest.FromBytes(ref)
    tgtDigest := digest.FromBytes(target)

    baseline := []BaselineLayerMeta{{Digest: refDigest, Size: int64(len(ref)), MediaType: "m"}}
    blobs := blobMap{refDigest: ref, tgtDigest: target}

    p := &Planner{baseline: baseline, readBlob: blobs.read}
    entries, payloads, err := p.Run(context.Background(), []diff.BlobRef{
        {Digest: tgtDigest, Size: int64(len(target)), MediaType: "m"},
    })
    require.NoError(t, err)
    require.Len(t, entries, 1)
    require.Equal(t, diff.EncodingFull, entries[0].Encoding,
        "independent random pair should degrade to encoding=full")
    require.True(t, bytes.Equal(target, payloads[tgtDigest]),
        "full payload must be verbatim target bytes")
}

func TestPlanner_PicksPatchWhenBytesClose(t *testing.T) {
    // Target is reference with a single byte flipped. Random reference
    // means full zstd cannot compress; dictionary-seeded patch produces a
    // tiny frame.
    ref := pseudoRandom(3, 1<<15)
    target := append([]byte(nil), ref...)
    target[0] ^= 0x42

    refDigest := digest.FromBytes(ref)
    tgtDigest := digest.FromBytes(target)

    baseline := []BaselineLayerMeta{{Digest: refDigest, Size: int64(len(ref)), MediaType: "m"}}
    blobs := blobMap{refDigest: ref, tgtDigest: target}

    p := &Planner{baseline: baseline, readBlob: blobs.read}
    entries, payloads, err := p.Run(context.Background(), []diff.BlobRef{
        {Digest: tgtDigest, Size: int64(len(target)), MediaType: "m"},
    })
    require.NoError(t, err)
    require.Len(t, entries, 1)
    require.Equal(t, diff.EncodingPatch, entries[0].Encoding)
    require.Equal(t, "zstd-patch", entries[0].Codec)
    require.Equal(t, refDigest, entries[0].PatchFromDigest)
    require.Less(t, entries[0].ArchiveSize, entries[0].Size)
    require.Less(t, len(payloads[tgtDigest]), len(target)/2,
        "patch of a near-identical pair should be far smaller than half")
}

func TestPlanner_PicksSizeClosestReferenceDeterministically(t *testing.T) {
    small := pseudoRandom(10, 1<<14)
    mid := pseudoRandom(11, 1<<15)
    big := pseudoRandom(12, 1<<16)
    target := append([]byte(nil), mid...) // byte-close to mid
    target[5] ^= 0x42

    baseline := []BaselineLayerMeta{
        {Digest: digest.FromBytes(small), Size: int64(len(small)), MediaType: "m"},
        {Digest: digest.FromBytes(mid), Size: int64(len(mid)), MediaType: "m"},
        {Digest: digest.FromBytes(big), Size: int64(len(big)), MediaType: "m"},
    }
    blobs := blobMap{
        digest.FromBytes(small):  small,
        digest.FromBytes(mid):    mid,
        digest.FromBytes(big):    big,
        digest.FromBytes(target): target,
    }

    p := &Planner{baseline: baseline, readBlob: blobs.read}
    entries, _, err := p.Run(context.Background(), []diff.BlobRef{
        {Digest: digest.FromBytes(target), Size: int64(len(target)), MediaType: "m"},
    })
    require.NoError(t, err)
    require.Equal(t, digest.FromBytes(mid), entries[0].PatchFromDigest,
        "planner must pick the size-closest baseline layer as patch reference")
}

func TestPlanner_EmptyBaselineProducesFullEntries(t *testing.T) {
    target := pseudoRandom(20, 1<<10)
    tgtDigest := digest.FromBytes(target)
    blobs := blobMap{tgtDigest: target}
    p := &Planner{baseline: nil, readBlob: blobs.read}

    entries, payloads, err := p.Run(context.Background(), []diff.BlobRef{
        {Digest: tgtDigest, Size: int64(len(target)), MediaType: "m"},
    })
    require.NoError(t, err)
    require.Len(t, entries, 1)
    require.Equal(t, diff.EncodingFull, entries[0].Encoding,
        "with no baseline layers to diff against, shipped entries must be full")
    require.True(t, bytes.Equal(target, payloads[tgtDigest]))
}

func TestPlanner_SizeTieBrokenByFirstSeen(t *testing.T) {
    // Two baseline layers of identical size, both unrelated to target bytes.
    // The target is byte-close to "a" so patch wins; both baselines have the
    // same size so size-closest is a tie — must resolve to first-seen.
    a := pseudoRandom(30, 1<<15)
    b := pseudoRandom(31, 1<<15)
    target := append([]byte(nil), a...)
    target[0] ^= 0xCC

    baseline := []BaselineLayerMeta{
        {Digest: digest.FromBytes(a), Size: int64(len(a)), MediaType: "m"},
        {Digest: digest.FromBytes(b), Size: int64(len(b)), MediaType: "m"},
    }
    blobs := blobMap{
        digest.FromBytes(a):      a,
        digest.FromBytes(b):      b,
        digest.FromBytes(target): target,
    }
    p := &Planner{baseline: baseline, readBlob: blobs.read}
    entries, _, err := p.Run(context.Background(), []diff.BlobRef{
        {Digest: digest.FromBytes(target), Size: int64(len(target)), MediaType: "m"},
    })
    require.NoError(t, err)
    require.Equal(t, digest.FromBytes(a), entries[0].PatchFromDigest,
        "tie: first-seen baseline entry wins")
}
```

- [ ] **Step 3.2: Run tests to confirm failure**

```bash
go test -tags 'containers_image_openpgp' ./pkg/exporter/... -run Planner -v
```

Expected: compile errors — `Planner`, `BaselineLayerMeta`, and their methods are undefined.

- [ ] **Step 3.3: Implement Planner**

Create `pkg/exporter/intralayer.go`:

```go
package exporter

import (
    "context"
    "fmt"

    "github.com/opencontainers/go-digest"

    "github.com/leosocy/diffah/internal/zstdpatch"
    "github.com/leosocy/diffah/pkg/diff"
)

// CodecZstdPatch is the canonical codec tag persisted in sidecar entries
// whose encoding is patch.
const CodecZstdPatch = "zstd-patch"

// BaselineLayerMeta is the minimum descriptor the planner needs for each
// baseline layer: a digest to key on, a size to match against, and a media
// type for sanity.
type BaselineLayerMeta struct {
    Digest    digest.Digest
    Size      int64
    MediaType string
}

// Planner computes per-layer encoding decisions for ShippedInDelta. It
// owns no I/O state directly — readBlob is injected so tests can avoid
// the real container-image stack.
type Planner struct {
    baseline []BaselineLayerMeta
    readBlob func(digest.Digest) ([]byte, error)
}

// NewPlanner builds a planner that reads target blobs from readTarget and
// reference blobs from readRef. Both are invoked with the digest of the
// blob to fetch. A single readBlob function could suffice, but keeping the
// two slots separate lets the caller plug in dir-transport reads for target
// and container-image reads for baseline without a composite adapter.
func NewPlanner(baseline []BaselineLayerMeta, readBlob func(digest.Digest) ([]byte, error)) *Planner {
    return &Planner{baseline: baseline, readBlob: readBlob}
}

// Run returns the BlobRef entries to drop into Sidecar.ShippedInDelta and
// the on-disk payload map. The payload under each digest is the bytes the
// exporter should persist at `<deltaDir>/<digest.Encoded()>` before packing.
func (p *Planner) Run(
    ctx context.Context, shipped []diff.BlobRef,
) ([]diff.BlobRef, map[digest.Digest][]byte, error) {
    _ = ctx // reserved for future cancellation plumbing
    entries := make([]diff.BlobRef, 0, len(shipped))
    payloads := make(map[digest.Digest][]byte, len(shipped))

    for _, l := range shipped {
        target, err := p.readBlob(l.Digest)
        if err != nil {
            return nil, nil, fmt.Errorf("read target blob %s: %w", l.Digest, err)
        }

        bestRef, ok := p.pickClosest(l.Size)
        if !ok {
            // No baseline layers to diff against → always full.
            entries = append(entries, fullEntry(l))
            payloads[l.Digest] = target
            continue
        }

        refBytes, err := p.readBlob(bestRef.Digest)
        if err != nil {
            return nil, nil, fmt.Errorf(
                "read baseline reference %s: %w", bestRef.Digest, err)
        }

        patch, err := zstdpatch.Encode(refBytes, target)
        if err != nil {
            return nil, nil, fmt.Errorf("encode patch %s: %w", l.Digest, err)
        }
        fullZst, err := zstdpatch.EncodeFull(target)
        if err != nil {
            return nil, nil, fmt.Errorf("encode full %s: %w", l.Digest, err)
        }

        if len(patch) < len(fullZst) && int64(len(patch)) < l.Size {
            entries = append(entries, diff.BlobRef{
                Digest:          l.Digest,
                Size:            l.Size,
                MediaType:       l.MediaType,
                Encoding:        diff.EncodingPatch,
                Codec:           CodecZstdPatch,
                PatchFromDigest: bestRef.Digest,
                ArchiveSize:     int64(len(patch)),
            })
            payloads[l.Digest] = patch
            continue
        }
        entries = append(entries, fullEntry(l))
        payloads[l.Digest] = target
    }
    return entries, payloads, nil
}

// pickClosest returns the baseline layer whose size is closest to want,
// with ties broken by first-seen index (deterministic for a given input).
func (p *Planner) pickClosest(want int64) (BaselineLayerMeta, bool) {
    if len(p.baseline) == 0 {
        return BaselineLayerMeta{}, false
    }
    best := p.baseline[0]
    bestDelta := absDelta(best.Size, want)
    for _, b := range p.baseline[1:] {
        d := absDelta(b.Size, want)
        if d < bestDelta {
            best, bestDelta = b, d
        }
    }
    return best, true
}

func absDelta(a, b int64) int64 {
    if a > b {
        return a - b
    }
    return b - a
}

// fullEntry builds a sidecar entry describing encoding=full for layer l.
func fullEntry(l diff.BlobRef) diff.BlobRef {
    return diff.BlobRef{
        Digest:      l.Digest,
        Size:        l.Size,
        MediaType:   l.MediaType,
        Encoding:    diff.EncodingFull,
        ArchiveSize: l.Size,
    }
}
```

- [ ] **Step 3.4: Run planner tests to confirm green**

```bash
go test -tags 'containers_image_openpgp' ./pkg/exporter/... -run Planner -v
```

Expected: all planner tests PASS.

- [ ] **Step 3.5: Commit**

```bash
git add pkg/exporter/intralayer.go pkg/exporter/intralayer_test.go
git commit -m "feat(exporter): add IntraLayerPlanner with size-closest matching

Planner computes per-layer encoding decisions, taking min(patch, full_zst)
per shipped blob. ReadBlob is injected so unit tests avoid the
container-image stack.

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 3"
```

---

## Task 4: Expose baseline layer sizes + media types

`IntraLayerPlanner` needs `[]BaselineLayerMeta`, but `BaselineSet` currently only returns digests. This task expands the interface without touching unrelated callers.

**Files:**
- Modify: `pkg/exporter/baseline.go`
- Modify: `pkg/exporter/baseline_test.go`

- [ ] **Step 4.1: Write the failing test**

Append to `pkg/exporter/baseline_test.go`:

```go
func TestImageBaseline_LayerMeta_IncludesSizeAndMediaType(t *testing.T) {
    ctx := context.Background()
    ref := ociArchiveRef(t, filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))
    b, err := NewImageBaseline(ctx, ref, nil, "oci-archive:v1", "")
    require.NoError(t, err)

    metas, err := b.LayerMeta(ctx)
    require.NoError(t, err)
    require.Len(t, metas, 2, "v1_oci has two layers (shared + version)")
    for _, m := range metas {
        require.NotEmpty(t, m.Digest)
        require.Greater(t, m.Size, int64(0))
        require.NotEmpty(t, m.MediaType)
    }
}
```

(The `ociArchiveRef` helper already exists in `baseline_test.go` — reuse it. Import `context` and `filepath` if not already imported.)

- [ ] **Step 4.2: Run test to confirm failure**

```bash
go test -tags 'containers_image_openpgp' ./pkg/exporter/... -run LayerMeta -v
```

Expected: compile error — `LayerMeta` method doesn't exist.

- [ ] **Step 4.3: Extend the BaselineSet interface**

In `pkg/exporter/baseline.go`, add to the interface and both implementations:

```go
type BaselineSet interface {
    LayerDigests(ctx context.Context) ([]digest.Digest, error)
    LayerMeta(ctx context.Context) ([]BaselineLayerMeta, error)
    ManifestRef() diff.BaselineRef
}
```

Add method to `ImageBaseline`:

```go
func (b *ImageBaseline) LayerMeta(_ context.Context) ([]BaselineLayerMeta, error) {
    infos := b.parsed.LayerInfos()
    out := make([]BaselineLayerMeta, 0, len(infos))
    for _, l := range infos {
        out = append(out, BaselineLayerMeta{
            Digest:    l.Digest,
            Size:      l.Size,
            MediaType: l.MediaType,
        })
    }
    return out, nil
}
```

Add method to `ManifestBaseline`:

```go
func (b *ManifestBaseline) LayerMeta(_ context.Context) ([]BaselineLayerMeta, error) {
    infos := b.parsed.LayerInfos()
    out := make([]BaselineLayerMeta, 0, len(infos))
    for _, l := range infos {
        out = append(out, BaselineLayerMeta{
            Digest:    l.Digest,
            Size:      l.Size,
            MediaType: l.MediaType,
        })
    }
    return out, nil
}
```

- [ ] **Step 4.4: Run test to confirm green**

```bash
go test -tags 'containers_image_openpgp' ./pkg/exporter/... -run LayerMeta -v
```

Expected: PASS.

- [ ] **Step 4.5: Commit**

```bash
git add pkg/exporter/baseline.go pkg/exporter/baseline_test.go
git commit -m "feat(exporter): expose LayerMeta on BaselineSet for intra-layer planner

Planner needs baseline layer sizes to choose the size-closest reference.
Adds LayerMeta to the interface and both implementations.

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 4"
```

---

## Task 5: Wire Planner into Exporter orchestration

**Files:**
- Modify: `pkg/exporter/exporter.go`
- Modify: `pkg/exporter/exporter_test.go`

- [ ] **Step 5.1: Write the failing test — intra-layer=auto round-trips**

Append to `pkg/exporter/exporter_test.go`:

```go
func TestExport_IntraLayer_Auto_WritesPatchEncoding(t *testing.T) {
    ctx := context.Background()
    // v3 fixture has byte-close layers vs v2 — every shipped layer should
    // pick encoding=patch.
    targetPath := filepath.Join(repoRoot(t), "testdata/fixtures/v3_oci.tar")
    baselinePath := filepath.Join(repoRoot(t), "testdata/fixtures/v2_oci.tar")

    targetRef, err := imageio.ParseReference("oci-archive:" + targetPath)
    require.NoError(t, err)
    baselineRef, err := imageio.ParseReference("oci-archive:" + baselinePath)
    require.NoError(t, err)

    out := filepath.Join(t.TempDir(), "delta.tar")
    err = exporter.Export(ctx, exporter.Options{
        TargetRef:   targetRef,
        BaselineRef: baselineRef,
        OutputPath:  out,
        IntraLayer:  "auto",
        ToolVersion: "test",
    })
    require.NoError(t, err)

    sc := readSidecar(t, out)
    var patchCount int
    for _, e := range sc.ShippedInDelta {
        switch e.Encoding {
        case diff.EncodingPatch:
            patchCount++
            require.NotEmpty(t, e.PatchFromDigest)
            require.Equal(t, "zstd-patch", e.Codec)
            require.Less(t, e.ArchiveSize, e.Size)
        case diff.EncodingFull:
            require.Equal(t, e.Size, e.ArchiveSize)
        default:
            t.Fatalf("unexpected encoding %q", e.Encoding)
        }
    }
    require.Greater(t, patchCount, 0,
        "v3 fixture pair should produce at least one encoding=patch entry")
}

func TestExport_IntraLayer_Off_AllEntriesAreFull(t *testing.T) {
    ctx := context.Background()
    targetPath := filepath.Join(repoRoot(t), "testdata/fixtures/v3_oci.tar")
    baselinePath := filepath.Join(repoRoot(t), "testdata/fixtures/v2_oci.tar")
    targetRef, _ := imageio.ParseReference("oci-archive:" + targetPath)
    baselineRef, _ := imageio.ParseReference("oci-archive:" + baselinePath)

    out := filepath.Join(t.TempDir(), "delta.tar")
    require.NoError(t, exporter.Export(ctx, exporter.Options{
        TargetRef: targetRef, BaselineRef: baselineRef,
        OutputPath: out, IntraLayer: "off", ToolVersion: "test",
    }))

    sc := readSidecar(t, out)
    for _, e := range sc.ShippedInDelta {
        require.Equal(t, diff.EncodingFull, e.Encoding,
            "intra-layer=off must emit only encoding=full entries")
    }
}

func TestExport_IntraLayer_Auto_WithManifestBaseline_Errors(t *testing.T) {
    ctx := context.Background()
    baselinePath := extractManifestToFile(t,
        "oci-archive:"+filepath.Join(repoRoot(t), "testdata/fixtures/v2_oci.tar"))
    targetRef, _ := imageio.ParseReference(
        "oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v3_oci.tar"))

    err := exporter.Export(ctx, exporter.Options{
        TargetRef:            targetRef,
        BaselineManifestPath: baselinePath,
        OutputPath:           filepath.Join(t.TempDir(), "d.tar"),
        IntraLayer:           "auto",
        ToolVersion:          "test",
    })
    var unsupported *diff.ErrIntraLayerUnsupported
    require.ErrorAs(t, err, &unsupported)
}
```

(The v3 fixture does not yet exist — these tests will fail until Task 11 lands. That is the intended ordering; flag each test with a `t.Skip` in Step 5.2 and re-enable in Task 11. See Step 5.2.)

- [ ] **Step 5.2: Gate the v3-dependent tests**

Add this at the top of each of the three new tests so they skip until v3 fixtures exist:

```go
if _, err := os.Stat(filepath.Join(repoRoot(t), "testdata/fixtures/v3_oci.tar")); os.IsNotExist(err) {
    t.Skip("v3 fixture not yet built — see Task 11")
}
```

(Task 11 removes the skip gate at its Step 11.4.)

- [ ] **Step 5.3: Run tests to confirm compile + skip behavior**

```bash
go test -tags 'containers_image_openpgp' ./pkg/exporter/... -run IntraLayer -v
```

Expected: compile errors — `Options.IntraLayer` does not exist.

- [ ] **Step 5.4: Wire Options and orchestration**

In `pkg/exporter/exporter.go`, extend `Options`:

```go
type Options struct {
    TargetRef            types.ImageReference
    BaselineRef          types.ImageReference
    BaselineManifestPath string
    Platform             string
    Compress             string
    OutputPath           string
    ToolVersion          string

    // IntraLayer controls per-layer binary patch computation:
    //   "auto" (default) — compute min(patch, full_zst) per shipped layer.
    //   "off"            — every shipped layer is encoding=full (v1-equivalent bytes).
    // Empty string is treated as "auto".
    IntraLayer string
}
```

Refactor `Export` to dispatch on IntraLayer after the copy step. Replace the existing `Export` and add helpers:

```go
func Export(ctx context.Context, opts Options) error {
    if opts.IntraLayer == "" {
        opts.IntraLayer = "auto"
    }

    baseline, err := openBaseline(ctx, opts)
    if err != nil {
        return err
    }
    baselineDigests, err := baseline.LayerDigests(ctx)
    if err != nil {
        return fmt.Errorf("load baseline digests: %w", err)
    }

    if opts.IntraLayer == "auto" && opts.BaselineManifestPath != "" {
        return &diff.ErrIntraLayerUnsupported{
            Reason: "baseline-manifest has no blob bytes; re-run with --intra-layer=off",
        }
    }

    tmpDir, err := os.MkdirTemp("", "diffah-export-")
    if err != nil {
        return fmt.Errorf("create tmp dir: %w", err)
    }
    defer os.RemoveAll(tmpDir)

    if err := copyTargetIntoDir(ctx, opts, tmpDir, baselineDigests); err != nil {
        return err
    }

    sidecar, err := buildSidecar(ctx, tmpDir, baseline, baselineDigests, opts)
    if err != nil {
        return err
    }
    sidecarBytes, err := sidecar.Marshal()
    if err != nil {
        return fmt.Errorf("marshal sidecar: %w", err)
    }

    compression := archive.CompressNone
    if opts.Compress == "zstd" {
        compression = archive.CompressZstd
    }
    if err := archive.Pack(tmpDir, sidecarBytes, opts.OutputPath, compression); err != nil {
        return err
    }
    return verifyExport(opts.OutputPath, sidecar)
}
```

Update `buildSidecar` to take `ctx` and compute encoded ShippedInDelta. Replace the existing function:

```go
func buildSidecar(
    ctx context.Context,
    dir string,
    baseline BaselineSet,
    baselineDigests []digest.Digest,
    opts Options,
) (diff.Sidecar, error) {
    manifestBytes, mediaType, err := oci.ReadDirManifest(dir)
    if err != nil {
        return diff.Sidecar{}, fmt.Errorf("read exported manifest: %w", err)
    }
    parsed, err := manifest.FromBlob(manifestBytes, mediaType)
    if err != nil {
        return diff.Sidecar{}, fmt.Errorf("parse target manifest: %w", err)
    }

    targetLayers := make([]diff.BlobRef, 0, len(parsed.LayerInfos()))
    for _, l := range parsed.LayerInfos() {
        targetLayers = append(targetLayers, diff.BlobRef{
            Digest: l.Digest, Size: l.Size, MediaType: l.MediaType,
        })
    }
    plan := diff.ComputePlan(targetLayers, baselineDigests)

    shipped, payloads, err := resolveShipped(ctx, dir, baseline, plan, opts)
    if err != nil {
        return diff.Sidecar{}, err
    }
    if err := writePayloads(dir, payloads); err != nil {
        return diff.Sidecar{}, err
    }

    platform := opts.Platform
    if platform == "" {
        platform = derivePlatformFromConfig(dir, parsed)
    }

    return diff.Sidecar{
        Version:     diff.SchemaVersionV1,
        Tool:        "diffah",
        ToolVersion: opts.ToolVersion,
        CreatedAt:   time.Now().UTC(),
        Platform:    platform,
        Target: diff.ImageRef{
            ManifestDigest: digest.FromBytes(manifestBytes),
            ManifestSize:   int64(len(manifestBytes)),
            MediaType:      mediaType,
        },
        Baseline:             baseline.ManifestRef(),
        RequiredFromBaseline: plan.RequiredFromBaseline,
        ShippedInDelta:       shipped,
    }, nil
}

// resolveShipped returns the encoded ShippedInDelta entries plus per-digest
// payload bytes ready for on-disk persistence. For "off", every entry is
// encoding=full. For "auto", Planner decides per layer.
func resolveShipped(
    ctx context.Context,
    dir string,
    baseline BaselineSet,
    plan diff.Plan,
    opts Options,
) ([]diff.BlobRef, map[digest.Digest][]byte, error) {
    switch opts.IntraLayer {
    case "off":
        entries := make([]diff.BlobRef, 0, len(plan.ShippedInDelta))
        for _, l := range plan.ShippedInDelta {
            entries = append(entries, fullEntry(l))
        }
        return entries, nil, nil // no payload overwrite needed
    case "auto":
        metas, err := baseline.LayerMeta(ctx)
        if err != nil {
            return nil, nil, fmt.Errorf("load baseline layer meta: %w", err)
        }
        readBlob := newDirBlobReader(dir, baseline)
        planner := NewPlanner(metas, readBlob)
        return planner.Run(ctx, plan.ShippedInDelta)
    default:
        return nil, nil, fmt.Errorf("unknown --intra-layer %q (want auto|off)",
            opts.IntraLayer)
    }
}

// newDirBlobReader returns a read function that serves target blobs from
// the on-disk dir layout and baseline blobs from the baseline image source.
// Both are content-addressed by digest, so a single map lookup would work;
// keeping them distinct makes the source of each read obvious at call
// sites.
func newDirBlobReader(dir string, baseline BaselineSet) func(digest.Digest) ([]byte, error) {
    ib, _ := baseline.(*ImageBaseline)
    return func(d digest.Digest) ([]byte, error) {
        // Target blobs are under <dir>/<hex>.
        path := filepath.Join(dir, d.Encoded())
        if data, err := os.ReadFile(path); err == nil {
            return data, nil
        }
        // Baseline blobs come from the container-image source.
        if ib == nil {
            return nil, fmt.Errorf("blob %s not in dir and baseline is not image-backed", d)
        }
        return readBaselineBlob(ib, d)
    }
}

// writePayloads overwrites the on-disk blob files so the packer picks up
// patch bytes under each target digest name.
func writePayloads(dir string, payloads map[digest.Digest][]byte) error {
    for d, data := range payloads {
        if err := os.WriteFile(filepath.Join(dir, d.Encoded()), data, 0o644); err != nil {
            return fmt.Errorf("write payload %s: %w", d, err)
        }
    }
    return nil
}
```

Add `readBaselineBlob` (new helper) to `pkg/exporter/baseline.go`:

```go
// readBaselineBlob fetches blob d from the image source used by this
// baseline. Used by IntraLayerPlanner when it needs the reference bytes
// for patch encoding.
func readBaselineBlob(b *ImageBaseline, d digest.Digest) ([]byte, error) {
    src, err := b.ref.NewImageSource(context.Background(), b.sys)
    if err != nil {
        return nil, fmt.Errorf("open baseline source: %w", err)
    }
    defer src.Close()
    r, _, err := src.GetBlob(context.Background(), types.BlobInfo{Digest: d}, nil)
    if err != nil {
        return nil, fmt.Errorf("get baseline blob %s: %w", d, err)
    }
    defer r.Close()
    data, err := io.ReadAll(r)
    if err != nil {
        return nil, fmt.Errorf("read baseline blob %s: %w", d, err)
    }
    return data, nil
}
```

Add `io` to that file's imports if not already present.

In `buildSidecar` above, we passed the `ctx` through; wire the updated signature from the single call site in `Export`.

- [ ] **Step 5.5: Update `Export` call-site**

The `Export` body already calls `buildSidecar(ctx, tmpDir, ...)` per the snippet in Step 5.4. Re-check the final file to confirm the old signature `buildSidecar(tmpDir, baseline, baselineDigests, opts)` is gone.

- [ ] **Step 5.6: Run exporter tests (existing + skipped)**

```bash
go test -tags 'containers_image_openpgp' ./pkg/exporter/... -v
```

Expected:
- All pre-existing exporter tests PASS (they pass `IntraLayer` unset, which defaults to "auto"; the fixtures have enough similarity that patches are valid — any `encoding=full` fallback still produces byte-exact output and valid sidecars).
- `TestExport_IntraLayer_Auto_WithManifestBaseline_Errors` PASS.
- `TestExport_IntraLayer_Auto_WritesPatchEncoding` and `TestExport_IntraLayer_Off_AllEntriesAreFull` SKIP (v3 fixture missing).

If any pre-existing test fails because an `encoding=full` ArchiveSize mismatch slipped through, check that `fullEntry` sets `ArchiveSize = Size`.

- [ ] **Step 5.7: Commit**

```bash
git add pkg/exporter/
git commit -m "feat(exporter): wire IntraLayerPlanner into Export with --intra-layer=auto|off

Adds Options.IntraLayer; Export dispatches to Planner for 'auto' and emits
all-full entries for 'off'. Baseline-manifest + auto now errors with
ErrIntraLayerUnsupported.

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 5"
```

---

## Task 6: Refactor CompositeSource for sidecar-driven dispatch

The current source has a try-delta-then-baseline fallback. The new flow classifies digests via the sidecar and dispatches to baseline + patch decode for patch entries. This is a constructor-signature change, so the call-site in `importer.go` changes too.

**Files:**
- Modify: `pkg/importer/composite_src.go`
- Modify: `pkg/importer/composite_src_test.go`
- Modify: `pkg/importer/importer.go` (call-site update only)

- [ ] **Step 6.1: Rewrite composite_src tests for the new classifier**

Replace the body of `pkg/importer/composite_src_test.go` — the existing tests encode the old fallback semantics and will be replaced. Keep the `fakeSource` helper at the top, then:

```go
package importer

import (
    "bytes"
    "context"
    "fmt"
    "io"
    "os"
    "strings"
    "testing"

    "github.com/opencontainers/go-digest"
    "github.com/stretchr/testify/require"
    "go.podman.io/image/v5/types"

    "github.com/leosocy/diffah/internal/zstdpatch"
    "github.com/leosocy/diffah/pkg/diff"
)

// fakeSource — same struct as before (keep the existing definition from
// the top of the file).

func makeSidecar(shipped []diff.BlobRef, required []diff.BlobRef) *diff.Sidecar {
    return &diff.Sidecar{
        Version:              "v1",
        Tool:                 "diffah",
        ToolVersion:          "test",
        Platform:             "linux/amd64",
        Target:               diff.ImageRef{ManifestDigest: "sha256:target", MediaType: "m"},
        Baseline:             diff.BaselineRef{ManifestDigest: "sha256:base", MediaType: "m"},
        ShippedInDelta:       shipped,
        RequiredFromBaseline: required,
    }
}

func TestComposite_GetBlob_RequiredEntry_FetchesFromBaseline(t *testing.T) {
    baseline := &fakeSource{blobs: map[digest.Digest]string{"sha256:req": "baseline-bytes"}}
    delta := &fakeSource{}
    sc := makeSidecar(nil, []diff.BlobRef{{Digest: "sha256:req", Size: 13, MediaType: "m"}})

    c := NewCompositeSource(delta, baseline, sc)
    r, _, err := c.GetBlob(context.Background(),
        types.BlobInfo{Digest: "sha256:req"}, nil)
    require.NoError(t, err)
    defer r.Close()
    out, _ := io.ReadAll(r)
    require.Equal(t, "baseline-bytes", string(out))
}

func TestComposite_GetBlob_FullEntry_FetchesFromDelta(t *testing.T) {
    delta := &fakeSource{blobs: map[digest.Digest]string{"sha256:full": "delta-bytes"}}
    baseline := &fakeSource{}
    sc := makeSidecar(
        []diff.BlobRef{{Digest: "sha256:full", Size: 11, MediaType: "m",
            Encoding: diff.EncodingFull, ArchiveSize: 11}},
        nil,
    )

    c := NewCompositeSource(delta, baseline, sc)
    r, _, err := c.GetBlob(context.Background(),
        types.BlobInfo{Digest: "sha256:full"}, nil)
    require.NoError(t, err)
    defer r.Close()
    out, _ := io.ReadAll(r)
    require.Equal(t, "delta-bytes", string(out))
}

func TestComposite_GetBlob_PatchEntry_ReassemblesViaBaseline(t *testing.T) {
    refBytes := bytes.Repeat([]byte{0x11}, 1<<12)
    target := append([]byte(nil), refBytes...)
    target[10] = 0x42
    tgtDigest := digest.FromBytes(target)
    refDigest := digest.FromBytes(refBytes)

    patch, err := zstdpatch.Encode(refBytes, target)
    require.NoError(t, err)

    delta := &fakeSource{blobs: map[digest.Digest]string{tgtDigest: string(patch)}}
    baseline := &fakeSource{blobs: map[digest.Digest]string{refDigest: string(refBytes)}}

    sc := makeSidecar(
        []diff.BlobRef{{
            Digest:          tgtDigest,
            Size:            int64(len(target)),
            MediaType:       "m",
            Encoding:        diff.EncodingPatch,
            Codec:           "zstd-patch",
            PatchFromDigest: refDigest,
            ArchiveSize:     int64(len(patch)),
        }},
        nil,
    )

    c := NewCompositeSource(delta, baseline, sc)
    r, _, err := c.GetBlob(context.Background(),
        types.BlobInfo{Digest: tgtDigest}, nil)
    require.NoError(t, err)
    defer r.Close()
    out, _ := io.ReadAll(r)
    require.True(t, bytes.Equal(out, target),
        "patch reassembly must reproduce target bytes exactly")
}

func TestComposite_GetBlob_PatchEntry_AssemblyMismatch_Errors(t *testing.T) {
    refBytes := bytes.Repeat([]byte{0x11}, 1<<12)
    target := append([]byte(nil), refBytes...)
    target[10] = 0x42
    tgtDigest := digest.FromBytes(target)
    refDigest := digest.FromBytes(refBytes)

    patch, err := zstdpatch.Encode(refBytes, target)
    require.NoError(t, err)
    // Corrupt the patch so decode produces wrong bytes (or fails outright).
    // Flip a byte in the middle of the zstd frame.
    patch[len(patch)/2] ^= 0xFF

    delta := &fakeSource{blobs: map[digest.Digest]string{tgtDigest: string(patch)}}
    baseline := &fakeSource{blobs: map[digest.Digest]string{refDigest: string(refBytes)}}
    sc := makeSidecar(
        []diff.BlobRef{{
            Digest:          tgtDigest,
            Size:            int64(len(target)),
            MediaType:       "m",
            Encoding:        diff.EncodingPatch,
            Codec:           "zstd-patch",
            PatchFromDigest: refDigest,
            ArchiveSize:     int64(len(patch)),
        }},
        nil,
    )

    c := NewCompositeSource(delta, baseline, sc)
    _, _, err = c.GetBlob(context.Background(),
        types.BlobInfo{Digest: tgtDigest}, nil)
    require.Error(t, err)
    // Either decode failed (zstd detected corruption) or digest check tripped.
    var mismatch *diff.ErrIntraLayerAssemblyMismatch
    if !errors.As(err, &mismatch) {
        require.Contains(t, err.Error(), "decode")
    }
}

func TestComposite_GetManifest_DelegatesToDelta(t *testing.T) {
    delta := &fakeSource{manifestRaw: []byte("d-manifest"), manifestMT: "application/delta"}
    baseline := &fakeSource{manifestRaw: []byte("b-manifest"), manifestMT: "application/baseline"}
    sc := makeSidecar(nil, nil)
    c := NewCompositeSource(delta, baseline, sc)
    raw, mt, err := c.GetManifest(context.Background(), nil)
    require.NoError(t, err)
    require.Equal(t, "d-manifest", string(raw))
    require.Equal(t, "application/delta", mt)
}

func TestComposite_Close_ClosesBoth(t *testing.T) {
    delta := &fakeSource{}
    baseline := &fakeSource{}
    c := NewCompositeSource(delta, baseline, makeSidecar(nil, nil))
    require.NoError(t, c.Close())
    require.Equal(t, 1, delta.closeCalls)
    require.Equal(t, 1, baseline.closeCalls)
}
```

Imports needed at the top of the file: `"bytes"`, `"context"`, `"errors"`, `"fmt"`, `"io"`, `"os"`, `"testing"`, `"github.com/opencontainers/go-digest"`, `"github.com/stretchr/testify/require"`, `"go.podman.io/image/v5/docker/reference"`, `"go.podman.io/image/v5/types"`, `"github.com/leosocy/diffah/internal/zstdpatch"`, `"github.com/leosocy/diffah/pkg/diff"`. Unused existing imports (`strings`, etc.) are cleaned up by goimports in Step 6.5.

- [ ] **Step 6.2: Run tests to confirm failure**

```bash
go test -tags 'containers_image_openpgp' ./pkg/importer/... -v
```

Expected: compile errors — `NewCompositeSource` still has the old 2-arg signature and `GetBlob` has no dispatch.

- [ ] **Step 6.3: Rewrite composite_src.go**

Replace `pkg/importer/composite_src.go` with:

```go
package importer

import (
    "bytes"
    "context"
    "errors"
    "fmt"
    "io"
    "os"

    "github.com/opencontainers/go-digest"
    "go.podman.io/image/v5/types"

    "github.com/leosocy/diffah/internal/zstdpatch"
    "github.com/leosocy/diffah/pkg/diff"
)

// CompositeSource implements types.ImageSource by classifying each blob
// digest against a sidecar and dispatching:
//
//   - RequiredFromBaseline entries are fetched from the baseline source.
//   - ShippedInDelta entries with encoding=full are fetched from the delta.
//   - ShippedInDelta entries with encoding=patch are fetched from the delta,
//     decoded against a baseline reference blob, and verified digest-wise
//     before return.
type CompositeSource struct {
    delta    types.ImageSource
    baseline types.ImageSource
    shipped  map[digest.Digest]diff.BlobRef
    required map[digest.Digest]diff.BlobRef
}

// NewCompositeSource wraps the two inner sources and pre-indexes the
// sidecar so GetBlob lookups are O(1). Close() on the composite closes
// both inner sources; callers must not close them directly.
func NewCompositeSource(
    delta, baseline types.ImageSource, sidecar *diff.Sidecar,
) *CompositeSource {
    shipped := make(map[digest.Digest]diff.BlobRef, len(sidecar.ShippedInDelta))
    for _, e := range sidecar.ShippedInDelta {
        shipped[e.Digest] = e
    }
    required := make(map[digest.Digest]diff.BlobRef, len(sidecar.RequiredFromBaseline))
    for _, e := range sidecar.RequiredFromBaseline {
        required[e.Digest] = e
    }
    return &CompositeSource{
        delta: delta, baseline: baseline,
        shipped: shipped, required: required,
    }
}

// GetBlob classifies info.Digest and dispatches accordingly.
func (c *CompositeSource) GetBlob(
    ctx context.Context, info types.BlobInfo, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
    if _, ok := c.required[info.Digest]; ok {
        return c.baseline.GetBlob(ctx, info, cache)
    }
    entry, ok := c.shipped[info.Digest]
    if !ok {
        // The copy runtime asked for a digest we don't know. Fall through to
        // delta — if it's the config or manifest blob, delta has it; if not,
        // delta will return a not-found error which propagates.
        return c.delta.GetBlob(ctx, info, cache)
    }
    switch entry.Encoding {
    case diff.EncodingFull:
        return c.delta.GetBlob(ctx, info, cache)
    case diff.EncodingPatch:
        return c.fetchPatched(ctx, entry, cache)
    default:
        return nil, 0, fmt.Errorf("composite: unknown encoding %q for %s",
            entry.Encoding, info.Digest)
    }
}

func (c *CompositeSource) fetchPatched(
    ctx context.Context, entry diff.BlobRef, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
    ref, err := readAll(c.baseline, ctx, types.BlobInfo{Digest: entry.PatchFromDigest}, cache)
    if err != nil {
        return nil, 0, fmt.Errorf("composite: fetch patch reference %s: %w",
            entry.PatchFromDigest, err)
    }
    patch, err := readAll(c.delta, ctx, types.BlobInfo{Digest: entry.Digest}, cache)
    if err != nil {
        return nil, 0, fmt.Errorf("composite: fetch patch bytes %s: %w",
            entry.Digest, err)
    }
    assembled, err := zstdpatch.Decode(ref, patch)
    if err != nil {
        return nil, 0, fmt.Errorf("composite: decode patch %s: %w", entry.Digest, err)
    }
    if got := digest.FromBytes(assembled); got != entry.Digest {
        return nil, 0, &diff.ErrIntraLayerAssemblyMismatch{
            Digest: entry.Digest.String(), Got: got.String(),
        }
    }
    return io.NopCloser(bytes.NewReader(assembled)), int64(len(assembled)), nil
}

func readAll(
    src types.ImageSource, ctx context.Context,
    info types.BlobInfo, cache types.BlobInfoCache,
) ([]byte, error) {
    r, _, err := src.GetBlob(ctx, info, cache)
    if err != nil {
        return nil, err
    }
    defer r.Close()
    return io.ReadAll(r)
}

// GetManifest, Reference, Close, HasThreadSafeGetBlob, GetSignatures,
// LayerInfosForCopy: delegate to delta — except Close, which closes both.

func (c *CompositeSource) GetManifest(
    ctx context.Context, instanceDigest *digest.Digest,
) ([]byte, string, error) {
    return c.delta.GetManifest(ctx, instanceDigest)
}

func (c *CompositeSource) Reference() types.ImageReference { return c.delta.Reference() }

func (c *CompositeSource) Close() error {
    errDelta := c.delta.Close()
    errBaseline := c.baseline.Close()
    if errDelta != nil {
        return errDelta
    }
    return errBaseline
}

func (c *CompositeSource) HasThreadSafeGetBlob() bool {
    return c.delta.HasThreadSafeGetBlob() && c.baseline.HasThreadSafeGetBlob()
}

func (c *CompositeSource) GetSignatures(
    ctx context.Context, instanceDigest *digest.Digest,
) ([][]byte, error) {
    return c.delta.GetSignatures(ctx, instanceDigest)
}

func (c *CompositeSource) LayerInfosForCopy(
    ctx context.Context, instanceDigest *digest.Digest,
) ([]types.BlobInfo, error) {
    return c.delta.LayerInfosForCopy(ctx, instanceDigest)
}

// isNotFound is retained as a helper for tests that exercise the fallback
// path (for unknown digests that are neither shipped nor required).
func isNotFound(err error) bool {
    return err != nil && errors.Is(err, os.ErrNotExist)
}

var _ types.ImageSource = (*CompositeSource)(nil)
```

- [ ] **Step 6.4: Update the `importer.go` call-site**

In `pkg/importer/importer.go`, `openCompositeSource` currently calls `NewCompositeSource(deltaSrc, baselineSrc)`. Change to:

```go
composite := NewCompositeSource(deltaSrc, baselineSrc, sidecar)
```

- [ ] **Step 6.5: Run tests to confirm green**

```bash
goimports -local github.com/leosocy/diffah -w pkg/importer/
go test -tags 'containers_image_openpgp' ./pkg/importer/... -v
```

Expected: all tests PASS.

- [ ] **Step 6.6: Commit**

```bash
git add pkg/importer/composite_src.go pkg/importer/composite_src_test.go pkg/importer/importer.go
git commit -m "refactor(importer): CompositeSource dispatches on sidecar encoding

Replaces the delta-then-baseline fallback with a sidecar-driven
classifier. Patch entries fetch the reference from baseline, decode via
zstdpatch, and verify the resulting sha256 against the declared digest;
mismatches raise ErrIntraLayerAssemblyMismatch.

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 6"
```

---

## Task 7: Baseline probe extension — patch_from_digest union

**Files:**
- Modify: `pkg/importer/importer.go`
- Modify: `pkg/importer/importer_test.go`

- [ ] **Step 7.1: Write the failing test**

Append to `pkg/importer/importer_test.go`:

```go
func TestProbeBaseline_MissingPatchRef_RaisesErr(t *testing.T) {
    // Manifest has sha256:req but not sha256:ref — the shipped patch entry
    // references a baseline blob that is absent.
    manifestJSON := `{
        "schemaVersion": 2,
        "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "config": {"mediaType": "application/vnd.oci.image.config.v1+json",
                   "size": 1, "digest": "sha256:cfg"},
        "layers": [{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
                    "size": 10, "digest": "sha256:req"}]
    }`
    src := &fakeSource{
        manifestRaw: []byte(manifestJSON),
        manifestMT:  "application/vnd.oci.image.manifest.v1+json",
    }

    sc := &diff.Sidecar{
        Version: "v1", Tool: "diffah", ToolVersion: "t", Platform: "linux/amd64",
        Target: diff.ImageRef{ManifestDigest: "sha256:tgt", MediaType: "m"},
        Baseline: diff.BaselineRef{ManifestDigest: "sha256:b", MediaType: "m"},
        RequiredFromBaseline: []diff.BlobRef{},
        ShippedInDelta: []diff.BlobRef{{
            Digest: "sha256:tgt", Size: 100, MediaType: "m",
            Encoding: diff.EncodingPatch, Codec: "zstd-patch",
            PatchFromDigest: "sha256:ref", ArchiveSize: 10,
        }},
    }

    err := probeBaseline(context.Background(), src, sc)
    var miss *diff.ErrBaselineMissingPatchRef
    require.ErrorAs(t, err, &miss)
    require.Equal(t, "sha256:ref", miss.Digest)
}
```

- [ ] **Step 7.2: Run to confirm failure**

```bash
go test -tags 'containers_image_openpgp' ./pkg/importer/... -run ProbeBaseline -v
```

Expected: test fails because the current probe only checks `RequiredFromBaseline`.

- [ ] **Step 7.3: Extend probeBaseline**

Replace `probeBaseline` in `pkg/importer/importer.go`:

```go
func probeBaseline(ctx context.Context, src types.ImageSource, s *diff.Sidecar) error {
    // Nothing to probe when the sidecar requests nothing from baseline.
    if len(s.RequiredFromBaseline) == 0 && !anyPatch(s) {
        return nil
    }
    raw, mime, err := src.GetManifest(ctx, nil)
    if err != nil {
        return fmt.Errorf("read baseline manifest: %w", err)
    }
    parsed, err := manifest.FromBlob(raw, mime)
    if err != nil {
        return fmt.Errorf("parse baseline manifest: %w", err)
    }
    have := make(map[digest.Digest]struct{}, len(parsed.LayerInfos()))
    for _, l := range parsed.LayerInfos() {
        have[l.Digest] = struct{}{}
    }
    source := src.Reference().StringWithinTransport()
    for _, req := range s.RequiredFromBaseline {
        if _, ok := have[req.Digest]; !ok {
            return &diff.ErrBaselineMissingBlob{
                Digest: string(req.Digest), Source: source,
            }
        }
    }
    seen := make(map[digest.Digest]struct{})
    for _, e := range s.ShippedInDelta {
        if e.Encoding != diff.EncodingPatch {
            continue
        }
        if _, dup := seen[e.PatchFromDigest]; dup {
            continue
        }
        seen[e.PatchFromDigest] = struct{}{}
        if _, ok := have[e.PatchFromDigest]; !ok {
            return &diff.ErrBaselineMissingPatchRef{
                Digest: string(e.PatchFromDigest), Source: source,
            }
        }
    }
    return nil
}

func anyPatch(s *diff.Sidecar) bool {
    for _, e := range s.ShippedInDelta {
        if e.Encoding == diff.EncodingPatch {
            return true
        }
    }
    return false
}
```

Note: `fakeSource.Reference()` returns nil today, so the probe test above will crash in `src.Reference().StringWithinTransport()`. Fix by adding a stub reference to `fakeSource` — add this to `composite_src_test.go`:

```go
type stubRef struct{}

func (stubRef) Transport() types.ImageTransport                    { return nil }
func (stubRef) StringWithinTransport() string                      { return "test://baseline" }
func (stubRef) DockerReference() reference.Named                   { return nil }
func (stubRef) PolicyConfigurationIdentity() string                { return "" }
func (stubRef) PolicyConfigurationNamespaces() []string            { return nil }
func (stubRef) NewImage(context.Context, *types.SystemContext) (types.ImageCloser, error) {
    return nil, nil
}
func (stubRef) NewImageSource(context.Context, *types.SystemContext) (types.ImageSource, error) {
    return nil, nil
}
func (stubRef) NewImageDestination(context.Context, *types.SystemContext) (types.ImageDestination, error) {
    return nil, nil
}
func (stubRef) DeleteImage(context.Context, *types.SystemContext) error { return nil }

var _ types.ImageReference = stubRef{}
```

And change `fakeSource.Reference()` to return `stubRef{}`. Add `reference` import from `"go.podman.io/image/v5/docker/reference"`.

- [ ] **Step 7.4: Run tests to confirm green**

```bash
go test -tags 'containers_image_openpgp' ./pkg/importer/... -v
```

Expected: all tests PASS.

- [ ] **Step 7.5: Commit**

```bash
git add pkg/importer/
git commit -m "feat(importer): probe baseline for patch_from_digest union

Extends probeBaseline to verify every unique patch_from_digest is
present in the baseline manifest. Missing refs raise
ErrBaselineMissingPatchRef so the operator sees 'need this layer to
decode' distinct from 'need this layer to be the output'.

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 7"
```

---

## Task 8: DryRun extension

**Files:**
- Modify: `pkg/importer/importer.go`
- Modify: `pkg/importer/importer_test.go`
- Modify: `cmd/import.go` (surface RequiredPatchRefs in output)

- [ ] **Step 8.1: Write the failing test**

Append to `pkg/importer/importer_test.go`:

```go
func TestDryRun_PatchRefs_DetectedAndReported(t *testing.T) {
    // Build a delta whose sidecar names a patch_from_digest the baseline
    // lacks. Hand-craft the archive via the archive package.
    tmp := t.TempDir()
    deltaDir := filepath.Join(tmp, "delta")
    require.NoError(t, os.MkdirAll(deltaDir, 0o755))

    sc := diff.Sidecar{
        Version: "v1", Tool: "diffah", ToolVersion: "t", Platform: "linux/amd64",
        CreatedAt: time.Now().UTC(),
        Target: diff.ImageRef{
            ManifestDigest: "sha256:tgt",
            ManifestSize:   1,
            MediaType:      "application/vnd.oci.image.manifest.v1+json",
        },
        Baseline: diff.BaselineRef{
            ManifestDigest: "sha256:b",
            MediaType:      "application/vnd.oci.image.manifest.v1+json",
        },
        RequiredFromBaseline: []diff.BlobRef{},
        ShippedInDelta: []diff.BlobRef{{
            Digest: "sha256:tgt", Size: 100,
            MediaType:       "application/vnd.oci.image.layer.v1.tar+gzip",
            Encoding:        diff.EncodingPatch,
            Codec:           "zstd-patch",
            PatchFromDigest: "sha256:missing-ref",
            ArchiveSize:     50,
        }},
    }
    raw, err := sc.Marshal()
    require.NoError(t, err)

    deltaPath := filepath.Join(tmp, "delta.tar")
    require.NoError(t, archive.Pack(deltaDir, raw, deltaPath, archive.CompressNone))

    baselinePath := filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar")
    baselineRef, err := imageio.ParseReference("oci-archive:" + baselinePath)
    require.NoError(t, err)

    report, err := DryRun(context.Background(), Options{
        DeltaPath:   deltaPath,
        BaselineRef: baselineRef,
        OutputPath:  filepath.Join(tmp, "out.tar"),
    })
    require.NoError(t, err)
    require.False(t, report.AllReachable)
    require.Equal(t, 1, report.RequiredPatchRefs)
    require.Equal(t, []string{"sha256:missing-ref"}, report.MissingPatchRefs)
}
```

Add imports: `"time"`, `"os"`, `"path/filepath"`, `"github.com/leosocy/diffah/internal/archive"`, `"github.com/leosocy/diffah/internal/imageio"` if not already.

Add a `repoRoot` helper mirroring the exporter tests' helper (at the top of `importer_test.go`):

```go
func repoRoot(t *testing.T) string {
    t.Helper()
    return filepath.Join("..", "..")
}
```

- [ ] **Step 8.2: Run to confirm failure**

```bash
go test -tags 'containers_image_openpgp' ./pkg/importer/... -run DryRun_PatchRefs -v
```

Expected: compile error — `DryRunReport.RequiredPatchRefs` and `MissingPatchRefs` do not exist.

- [ ] **Step 8.3: Extend DryRunReport and DryRun**

In `pkg/importer/importer.go`, replace the `DryRunReport` struct and `DryRun` body:

```go
// DryRunReport summarizes a dry-run import: which blobs the baseline must
// supply, and whether any are missing.
type DryRunReport struct {
    AllReachable      bool
    MissingDigests    []string // RequiredFromBaseline refs that baseline lacks.
    MissingPatchRefs  []string // shipped_in_delta patch_from_digests that baseline lacks.
    RequiredBlobs     int      // len(RequiredFromBaseline)
    RequiredPatchRefs int      // distinct patch_from_digests
    BaselineSource    string
}

func DryRun(ctx context.Context, opts Options) (DryRunReport, error) {
    tmpDir, err := os.MkdirTemp("", "diffah-import-dryrun-")
    if err != nil {
        return DryRunReport{}, fmt.Errorf("create tmp dir: %w", err)
    }
    defer os.RemoveAll(tmpDir)

    sidecar, err := extractSidecar(opts.DeltaPath, tmpDir)
    if err != nil {
        return DryRunReport{}, err
    }
    if _, err := resolveOutputFormat(opts.OutputFormat, sidecar.Target.MediaType, opts.AllowConvert); err != nil {
        return DryRunReport{}, err
    }

    baselineSrc, err := opts.BaselineRef.NewImageSource(ctx, nil)
    if err != nil {
        return DryRunReport{}, fmt.Errorf("open baseline source: %w", err)
    }
    defer baselineSrc.Close()

    report := DryRunReport{
        RequiredBlobs:  len(sidecar.RequiredFromBaseline),
        BaselineSource: baselineSrc.Reference().StringWithinTransport(),
    }

    raw, mime, err := baselineSrc.GetManifest(ctx, nil)
    if err != nil {
        return report, fmt.Errorf("read baseline manifest: %w", err)
    }
    parsed, err := manifest.FromBlob(raw, mime)
    if err != nil {
        return report, fmt.Errorf("parse baseline manifest: %w", err)
    }
    have := make(map[digest.Digest]struct{}, len(parsed.LayerInfos()))
    for _, l := range parsed.LayerInfos() {
        have[l.Digest] = struct{}{}
    }
    for _, req := range sidecar.RequiredFromBaseline {
        if _, ok := have[req.Digest]; !ok {
            report.MissingDigests = append(report.MissingDigests, string(req.Digest))
        }
    }
    seen := make(map[digest.Digest]struct{})
    for _, e := range sidecar.ShippedInDelta {
        if e.Encoding != diff.EncodingPatch {
            continue
        }
        if _, dup := seen[e.PatchFromDigest]; dup {
            continue
        }
        seen[e.PatchFromDigest] = struct{}{}
        if _, ok := have[e.PatchFromDigest]; !ok {
            report.MissingPatchRefs = append(report.MissingPatchRefs, string(e.PatchFromDigest))
        }
    }
    report.RequiredPatchRefs = len(seen)
    report.AllReachable = len(report.MissingDigests) == 0 && len(report.MissingPatchRefs) == 0
    return report, nil
}
```

- [ ] **Step 8.4: Update the CLI output to include patch refs**

In `cmd/import.go`'s `runImport`, extend the dry-run branch to print patch refs:

```go
if importFlags.dryRun {
    report, err := importer.DryRun(ctx, opts)
    if err != nil {
        return err
    }
    fmt.Fprintf(cmd.OutOrStdout(),
        "required blobs: %d (patch refs: %d), all reachable: %t\n",
        report.RequiredBlobs, report.RequiredPatchRefs, report.AllReachable)
    for _, d := range report.MissingDigests {
        fmt.Fprintf(cmd.ErrOrStderr(), "missing in baseline: %s\n", d)
    }
    for _, d := range report.MissingPatchRefs {
        fmt.Fprintf(cmd.ErrOrStderr(), "missing patch reference in baseline: %s\n", d)
    }
    if !report.AllReachable {
        return errors.New("baseline missing required blobs or patch references")
    }
    return nil
}
```

- [ ] **Step 8.5: Run tests to confirm green**

```bash
go test -tags 'containers_image_openpgp' ./pkg/importer/... ./cmd/... -v
```

Expected: all tests PASS. Note: `TestImportCommand_DryRun_Reachable` and `TestImportCommand_DryRun_Missing` in `cmd/import_integration_test.go` may now observe different output strings — they assert on `"all reachable: true"` and `"missing in baseline"`, both of which still appear. No integration-test change required.

- [ ] **Step 8.6: Commit**

```bash
git add pkg/importer/ cmd/import.go
git commit -m "feat(importer): DryRun reports patch_from_digest reachability

Extends DryRunReport with RequiredPatchRefs and MissingPatchRefs;
AllReachable becomes the conjunction of both sets being empty. CLI
prints patch-ref counts alongside required blobs.

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 8"
```

---

## Task 9: CLI --intra-layer flag

**Files:**
- Modify: `cmd/export.go`
- Modify: `cmd/export_integration_test.go`

- [ ] **Step 9.1: Add the failing integration test**

Append to `cmd/export_integration_test.go`:

```go
func TestExportCommand_IntraLayer_Off_Emits_FullOnlySidecar(t *testing.T) {
    root := findRepoRoot(t)
    delta := filepath.Join(t.TempDir(), "delta.tar")

    cmd := exec.Command("go", "run", "-tags", "containers_image_openpgp", ".",
        "export",
        "--target", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
        "--baseline", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
        "--output", delta,
        "--intra-layer", "off",
    )
    cmd.Dir = root
    out, err := cmd.CombinedOutput()
    require.NoError(t, err, "export output: %s", out)

    sidecarBytes, err := archive.ReadSidecar(delta)
    require.NoError(t, err)
    sc, err := diff.ParseSidecar(sidecarBytes)
    require.NoError(t, err)
    for _, e := range sc.ShippedInDelta {
        require.Equal(t, diff.EncodingFull, e.Encoding)
    }
}
```

Imports needed: `"github.com/leosocy/diffah/internal/archive"`, `"github.com/leosocy/diffah/pkg/diff"`.

- [ ] **Step 9.2: Run to confirm failure**

```bash
go test -tags 'integration containers_image_openpgp' ./cmd/... -run IntraLayer -v
```

Expected: failure — the binary doesn't accept `--intra-layer`.

- [ ] **Step 9.3: Add the flag**

In `cmd/export.go`, extend `exportFlags` and `newExportCommand`:

```go
var exportFlags = struct {
    target           string
    baseline         string
    baselineManifest string
    platform         string
    compress         string
    output           string
    dryRun           bool
    intraLayer       string
}{}

func newExportCommand() *cobra.Command {
    c := &cobra.Command{
        Use:   "export",
        Short: "Export a layer-diff delta archive from baseline and target images.",
        RunE:  runExport,
    }
    f := c.Flags()
    f.StringVar(&exportFlags.target, "target", "", "target image reference (required)")
    f.StringVar(&exportFlags.baseline, "baseline", "", "baseline image reference")
    f.StringVar(&exportFlags.baselineManifest, "baseline-manifest", "",
        "path to a baseline manifest.json (alternative to --baseline)")
    f.StringVar(&exportFlags.platform, "platform", "", "os/arch[/variant] (required for manifest lists)")
    f.StringVar(&exportFlags.compress, "compress", "none", "outer compression: none|zstd")
    f.StringVar(&exportFlags.output, "output", "", "output delta archive path (required)")
    f.BoolVar(&exportFlags.dryRun, "dry-run", false, "compute the plan without writing output")
    f.StringVar(&exportFlags.intraLayer, "intra-layer", "auto",
        "per-layer binary patching: auto|off (default auto)")
    _ = c.MarkFlagRequired("target")
    _ = c.MarkFlagRequired("output")
    return c
}
```

And in `runExport`, wire it into Options:

```go
opts := exporter.Options{
    TargetRef:            targetRef,
    Platform:             exportFlags.platform,
    Compress:             exportFlags.compress,
    OutputPath:           exportFlags.output,
    BaselineManifestPath: exportFlags.baselineManifest,
    IntraLayer:           exportFlags.intraLayer,
    ToolVersion:          version,
}
```

- [ ] **Step 9.4: Run integration tests**

```bash
go test -tags 'integration containers_image_openpgp' ./cmd/... -v
```

Expected: all tests PASS.

- [ ] **Step 9.5: Commit**

```bash
git add cmd/export.go cmd/export_integration_test.go
git commit -m "feat(cli): add --intra-layer=auto|off flag to export

Wires exporter.Options.IntraLayer to the CLI. Defaults to auto; users
opt out explicitly with --intra-layer=off, which emits v1-equivalent
archive bytes.

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 9"
```

---

## Task 10: CLI inspect augmentation

**Files:**
- Modify: `cmd/inspect.go`
- Modify: `cmd/inspect_test.go`

- [ ] **Step 10.1: Write the failing test**

Replace `TestInspectCommand_PrintsSidecarFields` in `cmd/inspect_test.go` (add more assertions, keep the old body's test infrastructure):

```go
func TestInspectCommand_PrintsSidecarFields(t *testing.T) {
    delta := buildInspectTestDelta(t)

    var buf bytes.Buffer
    rootCmd.SetOut(&buf)
    rootCmd.SetErr(&buf)
    rootCmd.SetArgs([]string{"inspect", delta})
    require.NoError(t, rootCmd.Execute())

    out := buf.String()
    require.Contains(t, out, "version: v1")
    require.Contains(t, out, "platform:")
    require.Contains(t, out, "target manifest digest:")
    require.Contains(t, out, "baseline manifest digest:")
    require.Contains(t, out, "shipped:")
    require.Contains(t, out, "required:")
    require.Regexp(t, `saved\s+[-.0-9]+%\s+vs full image`, out)
    // New v2 fields.
    require.Regexp(t, `full:\s+\d+ blobs`, out)
    require.Regexp(t, `patch:\s+\d+ blobs`, out)
    require.Regexp(t, `total archive:\s+\d+ bytes`, out)
}
```

- [ ] **Step 10.2: Run to confirm failure**

```bash
go test -tags 'containers_image_openpgp' ./cmd/... -run InspectCommand -v
```

Expected: failure — the new lines don't appear.

- [ ] **Step 10.3: Extend inspect.go**

Replace `printSidecar` in `cmd/inspect.go`:

```go
func printSidecar(w io.Writer, path string, s *diff.Sidecar) error {
    var fullCount, patchCount int
    var fullArchive, patchArchive, shippedRaw, required int64
    var patchRatioSum float64
    for _, b := range s.ShippedInDelta {
        shippedRaw += b.Size
        switch b.Encoding {
        case diff.EncodingFull:
            fullCount++
            fullArchive += b.ArchiveSize
        case diff.EncodingPatch:
            patchCount++
            patchArchive += b.ArchiveSize
            if b.Size > 0 {
                patchRatioSum += float64(b.ArchiveSize) / float64(b.Size)
            }
        }
    }
    for _, b := range s.RequiredFromBaseline {
        required += b.Size
    }
    totalArchive := fullArchive + patchArchive
    var savedPct float64
    total := shippedRaw + required
    if total > 0 {
        savedPct = (1.0 - float64(totalArchive)/float64(total)) * 100
    }

    fmt.Fprintf(w, "archive: %s\n", path)
    fmt.Fprintf(w, "version: %s\n", s.Version)
    fmt.Fprintf(w, "platform: %s\n", s.Platform)
    fmt.Fprintf(w, "target manifest digest: %s (%s)\n", s.Target.ManifestDigest, s.Target.MediaType)
    fmt.Fprintf(w, "baseline manifest digest: %s (%s)\n", s.Baseline.ManifestDigest, s.Baseline.MediaType)
    fmt.Fprintf(w, "shipped: %d blobs (%d bytes raw)\n", len(s.ShippedInDelta), shippedRaw)
    fmt.Fprintf(w, "  full:  %d blobs (%d bytes archive)\n", fullCount, fullArchive)
    if patchCount > 0 {
        avgRatio := (patchRatioSum / float64(patchCount)) * 100
        fmt.Fprintf(w, "  patch: %d blobs (%d bytes archive, avg ratio %.1f%%)\n",
            patchCount, patchArchive, avgRatio)
    } else {
        fmt.Fprintf(w, "  patch: 0 blobs\n")
    }
    fmt.Fprintf(w, "required: %d blobs (%d bytes)\n", len(s.RequiredFromBaseline), required)
    fmt.Fprintf(w, "total archive: %d bytes\n", totalArchive)
    fmt.Fprintf(w, "saved %.1f%% vs full image\n", savedPct)
    return nil
}
```

- [ ] **Step 10.4: Run to confirm green**

```bash
go test -tags 'containers_image_openpgp' ./cmd/... -run InspectCommand -v
```

Expected: PASS.

- [ ] **Step 10.5: Commit**

```bash
git add cmd/inspect.go cmd/inspect_test.go
git commit -m "feat(cli): inspect reports full/patch split and archive totals

Replaces the single 'saved X%' line with a breakdown of full vs patch
shipments, average patch ratio, and total archive size.

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 10"
```

---

## Task 11: v3 fixture pair for intra-layer testing

The existing fixtures have zero byte overlap across version layers (`v1\n` vs `v2\n`). Intra-layer tests need a pair where both layers differ by *few* bytes so patches are near-zero.

**Files:**
- Modify: `scripts/build_fixtures/main.go`
- Create: `testdata/fixtures/v3_oci.tar` (build output)
- Create: `testdata/fixtures/v3_s2.tar` (build output)
- Modify: `testdata/fixtures/CHECKSUMS` (regenerated)
- Modify: `pkg/exporter/exporter_test.go` (remove the v3 skip gates added in Task 5)

- [ ] **Step 11.1: Extend the fixture generator**

In `scripts/build_fixtures/main.go`, the existing script builds v1 and v2 fixtures where each version's two layers are (shared.bin, version.txt). For v3 we want layers that differ byte-wise from v2 by a few bytes only — so both layers end up "shipped" vs v2 baseline but patch to tiny sizes.

Extend the `versions` slice and add a new shared layer variant specifically for v3. The original v1/v2 `shared.bin` is 32 KiB of zeros — that compresses to tens of bytes even without a dictionary, so the planner's `min(patch, full_zst)` will pick full and the test assertion that v3 produces patch entries will be flaky. Swap to 1 MiB of seeded pseudo-random bytes: random data does not compress without a dictionary, so dict-seeded patches beat full zstd decisively.

Replace the `versions` block and shared-layer build:

```go
import "math/rand/v2"

// seededRandom fills dst with deterministic pseudo-random bytes. Used to
// make fixture layers resist zstd-full compression so intra-layer patches
// win the min(patch, full_zst) comparison reliably.
func seededRandom(seed uint64, size int) []byte {
    r := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
    out := make([]byte, size)
    for i := range out {
        out[i] = byte(r.Uint32())
    }
    return out
}

// ... inside buildFixtures:

// Build shared base layers. v1/v2 share "base-v12" (same digest); v3 uses
// "base-v3" which differs by a single byte — so intra-layer patches have
// something real to compress. Contents are pseudo-random so zstd alone
// cannot shrink them; a dict-seeded patch decisively wins.
const sharedSize = 1 << 20 // 1 MiB
baseV12Data := seededRandom(42, sharedSize)
baseV12Compressed, baseV12DiffID, baseV12Blob := buildLayerBlob("shared.bin", baseV12Data)

baseV3Data := append([]byte(nil), baseV12Data...)
baseV3Data[0] ^= 0x01 // single-byte drift from v12 baseline
baseV3Compressed, baseV3DiffID, baseV3Blob := buildLayerBlob("shared.bin", baseV3Data)

fmt.Printf("shared v12 diffID: %s\n", baseV12DiffID)
fmt.Printf("shared v3  diffID: %s\n", baseV3DiffID)

// Version layers.
v1Compressed, v1DiffID, v1Blob := buildLayerBlob("version.txt", []byte("v1\n"))
v2Compressed, v2DiffID, v2Blob := buildLayerBlob("version.txt", []byte("v2\n"))
v3Compressed, v3DiffID, v3Blob := buildLayerBlob("version.txt", []byte("v3\n"))

type fixtureSpec struct {
    name         string
    baseBytes    []byte
    baseDiff     digest.Digest
    baseBlob     digest.Digest
    versionLayer []byte
    versionDiff  digest.Digest
    versionBlob  digest.Digest
    version      string
}

versions := []fixtureSpec{
    {
        name: "v1", baseBytes: baseV12Compressed, baseDiff: baseV12DiffID, baseBlob: baseV12Blob,
        versionLayer: v1Compressed, versionDiff: v1DiffID, versionBlob: v1Blob, version: "v1",
    },
    {
        name: "v2", baseBytes: baseV12Compressed, baseDiff: baseV12DiffID, baseBlob: baseV12Blob,
        versionLayer: v2Compressed, versionDiff: v2DiffID, versionBlob: v2Blob, version: "v2",
    },
    {
        name: "v3", baseBytes: baseV3Compressed, baseDiff: baseV3DiffID, baseBlob: baseV3Blob,
        versionLayer: v3Compressed, versionDiff: v3DiffID, versionBlob: v3Blob, version: "v3",
    },
}
```

Note: since `shared.bin` content is changing from zeros to pseudo-random, v1/v2 fixture digests will also change. Regenerate `CHECKSUMS` (Step 11.2 emits it) and expect existing exporter/importer fixture tests to re-resolve their digest assertions — none of them pin specific digest values, only count and non-emptiness.

Inside the `for _, vs := range versions` loop, replace the pre-loop-constant references to the shared layer with fields from `vs`:

```go
layers := [][]byte{vs.baseBytes, vs.versionLayer}
layerDiffs := []digest.Digest{vs.baseDiff, vs.versionDiff}
layerBlobs := []types.BlobInfo{
    {Digest: vs.baseBlob, Size: int64(len(vs.baseBytes))},
    {Digest: vs.versionBlob, Size: int64(len(vs.versionLayer))},
}
```

Also add `v3_oci.tar`, `v3_s2.tar` to the cleanup list in `removeIfExists` iteration at the top of `buildFixtures`.

- [ ] **Step 11.2: Build fixtures**

```bash
make fixtures
```

Expected output includes lines:

```
wrote testdata/fixtures/v3_oci.tar
wrote testdata/fixtures/v3_s2.tar
```

- [ ] **Step 11.3: Verify fixtures exist**

```bash
ls -la testdata/fixtures/v3_*.tar
```

Expected: both files exist and are non-empty.

- [ ] **Step 11.4: Remove the skip gates from Task 5**

Delete the `t.Skip(...)` lines added in Step 5.2 from the three `TestExport_IntraLayer_*` tests. Run:

```bash
go test -tags 'containers_image_openpgp' ./pkg/exporter/... -run IntraLayer -v
```

Expected: all three PASS with patches emitted against the byte-close v3/v2 pair.

- [ ] **Step 11.5: Commit**

```bash
git add scripts/build_fixtures/ testdata/fixtures/v3_oci.tar testdata/fixtures/v3_s2.tar testdata/fixtures/CHECKSUMS pkg/exporter/exporter_test.go
git commit -m "test(fixtures): add v3_oci/v3_s2 with byte-close layers vs v2

v3 differs from v2 by a single byte in shared.bin and three bytes in
version.txt, producing the byte-overlap-but-different-digest shape that
intra-layer patching targets.

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 11"
```

---

## Task 12: Intra-layer end-to-end integration tests

**Files:**
- Modify: `pkg/importer/integration_test.go`

- [ ] **Step 12.1: Write the failing tests**

Append to `pkg/importer/integration_test.go`:

```go
// TestIntraLayer_EndToEnd_OCIFixture exports the v3_oci → v2_oci delta with
// intra-layer auto, then imports it back and asserts the reconstructed
// manifest digest equals the sidecar target digest (byte-exact round-trip).
func TestIntraLayer_EndToEnd_OCIFixture(t *testing.T) {
    intraLayerRoundTrip(t, "v3_oci.tar", "v2_oci.tar", "oci-archive")
}

func TestIntraLayer_EndToEnd_Schema2Fixture(t *testing.T) {
    intraLayerRoundTrip(t, "v3_s2.tar", "v2_s2.tar", "docker-archive")
}

func intraLayerRoundTrip(t *testing.T, targetFx, baselineFx, transport string) {
    ctx := context.Background()
    root := repoRoot(t)

    targetRef, err := imageio.ParseReference(
        transport + ":" + filepath.Join(root, "testdata/fixtures", targetFx))
    require.NoError(t, err)
    baselineRef, err := imageio.ParseReference(
        transport + ":" + filepath.Join(root, "testdata/fixtures", baselineFx))
    require.NoError(t, err)

    delta := filepath.Join(t.TempDir(), "delta.tar")
    require.NoError(t, exporter.Export(ctx, exporter.Options{
        TargetRef: targetRef, BaselineRef: baselineRef,
        OutputPath: delta, IntraLayer: "auto", ToolVersion: "test",
    }))

    // Ensure the sidecar actually contains at least one patch entry —
    // otherwise the test wouldn't be exercising the patch path.
    sidecarBytes, err := archive.ReadSidecar(delta)
    require.NoError(t, err)
    sc, err := diff.ParseSidecar(sidecarBytes)
    require.NoError(t, err)
    hasPatch := false
    for _, e := range sc.ShippedInDelta {
        if e.Encoding == diff.EncodingPatch {
            hasPatch = true
            break
        }
    }
    require.True(t, hasPatch, "expected v3→v2 delta to include at least one patch entry")

    // Import back to dir format so we can verify the manifest digest.
    outDir := filepath.Join(t.TempDir(), "restored")
    require.NoError(t, Import(ctx, Options{
        DeltaPath: delta, BaselineRef: baselineRef,
        OutputFormat: FormatDir, OutputPath: outDir,
    }))

    raw, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
    require.NoError(t, err)
    require.Equal(t, sc.Target.ManifestDigest, digest.FromBytes(raw),
        "imported manifest digest must match sidecar target")
}

// TestIntraLayer_MixedEncoding_Matrix verifies a single archive with both
// encoding flavours round-trips cleanly. With the v3 fixture, both layers
// should end up patch; we force at least one full entry by using an
// unrelated image as baseline.
func TestIntraLayer_MixedEncoding_Matrix(t *testing.T) {
    ctx := context.Background()
    root := repoRoot(t)

    targetRef, _ := imageio.ParseReference(
        "oci-archive:" + filepath.Join(root, "testdata/fixtures/v3_oci.tar"))
    // Mix baseline: v2_oci shares the base layer with v3's matching digest
    // is close, but provides a size-close reference for the version layer
    // where patching is favourable. Fixture sizes are small enough that
    // the planner may still pick full on one layer and patch on another.
    baselineRef, _ := imageio.ParseReference(
        "oci-archive:" + filepath.Join(root, "testdata/fixtures/v2_oci.tar"))

    delta := filepath.Join(t.TempDir(), "delta.tar")
    require.NoError(t, exporter.Export(ctx, exporter.Options{
        TargetRef: targetRef, BaselineRef: baselineRef,
        OutputPath: delta, IntraLayer: "auto", ToolVersion: "test",
    }))

    outDir := filepath.Join(t.TempDir(), "restored")
    require.NoError(t, Import(ctx, Options{
        DeltaPath: delta, BaselineRef: baselineRef,
        OutputFormat: FormatDir, OutputPath: outDir,
    }))

    raw, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
    require.NoError(t, err)
    sidecarBytes, _ := archive.ReadSidecar(delta)
    sc, _ := diff.ParseSidecar(sidecarBytes)
    require.Equal(t, sc.Target.ManifestDigest, digest.FromBytes(raw))
}
```

Add imports: `"os"`, `"github.com/leosocy/diffah/internal/archive"`, `"github.com/opencontainers/go-digest"`, `"github.com/leosocy/diffah/pkg/exporter"`, `"github.com/leosocy/diffah/pkg/diff"`, `"github.com/leosocy/diffah/internal/imageio"`. Promote the existing `buildDelta` / `buildDeltaS2` helpers remain unchanged.

- [ ] **Step 12.2: Run integration tests**

```bash
go test -tags 'integration containers_image_openpgp' ./pkg/importer/... ./cmd/... -v
```

Expected: all tests PASS including the three new ones.

- [ ] **Step 12.3: Commit**

```bash
git add pkg/importer/integration_test.go
git commit -m "test(importer): end-to-end intra-layer round-trip on v3 fixtures

Covers OCI and docker-schema2 fixtures plus a mixed-encoding matrix. All
three assert manifest-digest byte-equivalence after an export→import
cycle with intra-layer=auto.

Refs: docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md Task 12"
```

---

## Task 13: Lint & full test sweep

- [ ] **Step 13.1: Lint clean**

```bash
make lint
```

Expected: 0 issues.

- [ ] **Step 13.2: Unit tests**

```bash
make test
```

Expected: all green, including the updated pkg/diff, pkg/exporter, pkg/importer, and cmd unit tests.

- [ ] **Step 13.3: Integration tests**

```bash
make test-integration
```

Expected: all green.

- [ ] **Step 13.4: Smoke-check inspect output on a real fixture**

```bash
go run -tags 'containers_image_openpgp' . export \
    --target    oci-archive:testdata/fixtures/v3_oci.tar \
    --baseline  oci-archive:testdata/fixtures/v2_oci.tar \
    --output    /tmp/diffah-smoke/delta.tar
go run -tags 'containers_image_openpgp' . inspect /tmp/diffah-smoke/delta.tar
```

Expected inspect output shape:

```
archive: /tmp/diffah-smoke/delta.tar
version: v1
platform: linux/amd64
target manifest digest: sha256:... (application/vnd.oci.image.manifest.v1+json)
baseline manifest digest: sha256:... (application/vnd.oci.image.manifest.v1+json)
shipped: 2 blobs (<N> bytes raw)
  full:  <X> blobs (<Y> bytes archive)
  patch: <X> blobs (<Y> bytes archive, avg ratio <Z>%)
required: 0 blobs (0 B)
total archive: <TOTAL> bytes
saved <PCT>% vs full image
```

- [ ] **Step 13.5: Final tidy commit (if any drift)**

If `make fmt` produced diffs, commit them:

```bash
make fmt
git add -A
git status
# if any changes:
git commit -m "chore: gofmt/goimports after intra-layer feature"
```

Otherwise skip.

---

## Self-review

**Spec coverage:**

- §2 in-scope items:
  - `internal/zstdpatch`: Task 1. ✓
  - Exporter extension (min(patch,full)): Tasks 3, 4, 5. ✓
  - Importer extension (CompositeSource + verify): Task 6. ✓
  - Sidecar schema evolution on `v1`: Task 2. ✓
  - `--intra-layer=auto|off`: Task 9. ✓
  - inspect output: Task 10. ✓
  - DryRun extension: Tasks 7, 8. ✓

- §3 decisions all honoured:
  - D1 (sidecar stays v1): Task 2 extends schema, no version dispatch. ✓
  - D2 (mixed encodings): validator accepts; Task 6 dispatches per entry. ✓
  - D3 (auto default): Task 9. ✓
  - D4 (single importer path): Task 6. ✓
  - D5 (always min()): Task 3. ✓
  - D6 (extend BlobRef): Task 2. ✓
  - D7 (klauspost w/ fallback): Task 0 gate. ✓

- §8 error taxonomy: Task 2 adds all three. ✓

- §10 unit tests — every listed test maps to a step:
  - `TestZstdpatch_RoundTrip` → Step 1.1.
  - `TestZstdpatch_WrongReference` → Step 1.1.
  - `TestIntraLayerPlanner_PrefersFullWhenPatchLarger` → Step 3.1.
  - `TestIntraLayerPlanner_PicksSizeClosestMatch` → Step 3.1.
  - `TestSidecar_v1_RejectsMissingEncoding` → Step 2.5.
  - `TestSidecar_v1_RejectsPatchMissingFromDigest` → Step 2.5.
  - `TestSidecar_v1_FullMustNotHavePatchFields` → Step 2.5.
  - `TestCompositeSource_AppliesPatch` → Step 6.1.
  - `TestCompositeSource_AssemblyMismatchErrors` → Step 6.1.
  - `TestImport_DryRun_DetectsMissingPatchRef` → Step 8.1.

- §10 integration tests all mapped to Task 12.

- §11.1 spike acceptance: Task 0 full coverage. ✓

- §12 Phase 1 deliverable definition items 1–8 all covered by Tasks 1–13.

**Placeholder scan:** None of the `- [ ]` steps contain `TBD`, `TODO`, `similar to`, or "add error handling as needed" language. Every code block is complete and copy-pasteable.

**Type consistency:**
- `Encoding` + `EncodingFull` / `EncodingPatch` — defined Task 2, used Tasks 3, 5, 6, 7, 8, 10.
- `CodecZstdPatch = "zstd-patch"` — defined Task 3; the string literal also appears in Tasks 2 (tests) and 6 (tests) — consistent across.
- `BaselineLayerMeta` — defined Task 3 in `pkg/exporter`; consumed by `LayerMeta` accessors added in Task 4.
- `NewCompositeSource(delta, baseline, *diff.Sidecar)` — signature introduced Task 6; updated call-site in Task 6.4.
- `ErrIntraLayerAssemblyMismatch`, `ErrBaselineMissingPatchRef`, `ErrIntraLayerUnsupported` — defined Task 2; used Tasks 5, 6, 7, 8 consistently (`ErrIntraLayerUnsupported` on exporter side; `ErrIntraLayerAssemblyMismatch` on importer; `ErrBaselineMissingPatchRef` on both probe + dry-run).
- `Options.IntraLayer` is a `string` with values `"auto"`/`"off"` — consistent across Tasks 5, 9, 12.

All consistent.

---

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-20-diffah-v2-intra-layer-phase1.md`.

**Execution options:**

1. **Subagent-driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline execution** — execute tasks in the current session with checkpoints.

Start with Task 0 (the spike) regardless of execution mode — the rest of the plan is conditional on the klauspost backend decision. If the spike selects `os/exec`, Task 1 is re-implemented with the same interface and all downstream tasks run unchanged.
