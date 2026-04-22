# diffah v2 — Content-Similarity Layer Matching (I.a)

**Date:** 2026-04-20
**Status:** Design approved, pending implementation plan
**Scope owner:** diffah core (leosocy)
**Phase:** v2 Phase 2 — deeper compression, increment #1
**Depends on:** Phase 1 intra-layer feature (merged)

## 1. Purpose and motivation

Phase 1 matches each target layer to its **size-closest** baseline layer
before computing a `zstd --patch-from` patch (`pkg/exporter/intralayer.go:100`).
Size is a cheap proxy for "same layer, rebuilt" — which is why the
initial POC already scored 45–96 % savings on rebuild scenarios — but
it fails on any pair where layers change *structurally* (added
dependency, file replaced, reorganisation). In those cases size-closest
silently picks a baseline whose bytes have nothing to do with the
target, produces a patch larger than full-zstd, and the
`min(patch, full)` guard falls back to full. The cost to the *end user*
is lost savings, not a broken archive — but on non-rebuild pairs the
wasted-patch rate (where the chosen baseline shares zero content with
the target and the planner has to fall back to full-zstd) is high
enough that fixing it is the highest-leverage next improvement to
Phase 1. The measurement spike in §9 quantifies this before we commit
the implementation work.

Content-similarity matching replaces the size heuristic with a direct
measurement of byte overlap between the target layer and each baseline
layer, computed over decompressed tar entries. On rebuild pairs the two
strategies converge (same-sized, same-content layers); on non-rebuild
pairs content-matching picks baselines that actually share bytes,
restoring Phase 1's compression ceiling on the harder cases.

This spec covers the internal matcher change only. No sidecar schema
change, no archive format change, no CLI flag, no cross-image matching
(that belongs to spec II.c).

## 2. Scope and non-goals

**In scope:**

- A `Fingerprint` type and `Fingerprinter` interface in
  `pkg/exporter/fingerprint.go`, hashing every `TypeReg` tar entry by
  `sha256` and mapping the digest to its uncompressed size.
- A `DefaultFingerprinter` that decompresses OCI layers by media type
  suffix (`+gzip`, `+zstd`, plain) and streams through `archive/tar`.
- Planner upgrade: eager baseline fingerprinting in `NewPlanner`; new
  `pickSimilar` replaces `pickClosest` as the primary picker;
  `pickClosest` remains as the named fallback.
- Typed fingerprinting errors surfaced through `ErrFingerprintFailed`
  so planner branches are testable without string matching.
- Unit + fixture tests covering the decision tree (seven branches),
  malformed inputs, and decompressor matrix.
- A new `v4` fixture pair where size-closest and content-match
  provably diverge.
- A throwaway measurement spike (`scripts/contentsim-spike/main.go`)
  gated as Task 0 of the implementation plan.

**Explicitly out of scope:**

- Alternative matching algorithms (FastCDC chunking, simhash,
  manifest-history-based matching). Revisit only if the measurement
  spike shows tar-entry intersection is insufficient.
- User-visible `--layer-match` flag. Added only if the spike reveals a
  regression case worth an escape hatch.
- Persistent fingerprint caching across runs. No on-disk state added.
- Cross-image fingerprint sharing. Covered in spec II.c.
- Path-aware (`(path, digest)`) fingerprinting. Content-only chosen
  below (§3 #3).
- Streaming fingerprinter for GB-scale layers. Tied to Track III
  (production hardening).

## 3. Decision log

| # | Decision | Reason |
|---|---|---|
| 1 | Matching algorithm: byte-weighted tar-entry digest intersection. | OCI tar is the natural content unit; per-entry SHA-256 is already a standard primitive; tar parsing is stdlib-only. Alternatives (FastCDC, simhash) carry new deps and/or weaker signals. |
| 2 | Eager baseline fingerprinting at planner init. | Baseline sets are small (≤ ~20 layers) and read once anyway; eager cost is bounded and predictable; avoids state complexity of lazy fingerprinting. |
| 3 | Fingerprint is content-only (no path). | Rebuilds often rename or re-home files; what patch-from cares about is shared *bytes*, not shared paths. Content-only is strictly more permissive without costing measurable accuracy. |
| 4 | Byte-weighted intersection, not file-count intersection. | One shared 100 MB binary dwarfs ten shared 1 KB config files for patch-from purposes. Weighting matches zstd's window semantics. |
| 5 | Duplicated content (same digest at multiple paths) counts once. | zstd's window compresses duplicates to near-zero; weighting each instance would over-count. |
| 6 | Transparent switch — no user-visible flag. | The algorithm is an internal optimisation; if it's never worse than size-closest in measured scenarios, there's nothing to tune. Add a flag only if the spike shows a regression case. |
| 7 | Deterministic tie-break: max-score, then size-closest, then first-seen index. | Archive reproducibility matters for integration tests and operator trust. Sort baselines by digest before scoring to defeat Go map randomisation. |
| 8 | Fingerprinting failure on target or a baseline is non-fatal. | Drops to size-closest for the affected layer; keeps the feature forward-compatible with OCI quirks (non-tar configs, foreign layers). |
| 9 | Measurement spike is a plan-level gate, not a code-level gate. | If the spike shows content-match never beats size-closest on real images, we don't ship the feature. Decision belongs in the plan, not in runtime fallback logic. |
| 10 | No sidecar schema change. | `patch_from_digest` already records *which* baseline was chosen; *how* it was chosen is a pure-internal exporter concern. |

## 4. Architecture

### 4.1 Package layout

```
pkg/exporter/
  intralayer.go          Planner updated: new pickSimilar, pickClosest kept as fallback,
                         baselineFP map populated eagerly in NewPlanner
  intralayer_test.go     New decision-tree table; existing pickClosest tests preserved
  fingerprint.go         NEW — Fingerprint, Fingerprinter, DefaultFingerprinter,
                         ErrFingerprintFailed
  fingerprint_test.go    NEW — decompressor matrix + malformed-input coverage

scripts/
  contentsim-spike/main.go   NEW throwaway — runs on POC blobs, reports size-closest vs
                             content-match total patch bytes. Not built in CI.

testdata/fixtures/
  v4_oci.tar             NEW — constructed so the two strategies disagree
  v4_s2.tar              NEW — docker-schema-2 twin
```

### 4.2 Types

```go
// Fingerprint of a decompressed tar layer: for each distinct regular-file
// content digest, the size of one instance. Directories, symlinks, hard
// links, and special files are skipped — they contribute no real bytes
// to zstd's patch-from window.
type Fingerprint map[digest.Digest]int64

// Fingerprinter hashes a compressed layer blob into a Fingerprint. The
// media type picks the decompressor; unknown or malformed input yields
// an error wrapping ErrFingerprintFailed.
type Fingerprinter interface {
    Fingerprint(ctx context.Context, mediaType string, blob []byte) (Fingerprint, error)
}

// DefaultFingerprinter handles the three media-type suffixes currently
// produced in the wild:
//   *+gzip → compress/gzip
//   *+zstd → klauspost/compress/zstd
//   other  → raw tar (no compression)
type DefaultFingerprinter struct{}

// ErrFingerprintFailed is the sentinel returned by any fingerprint step;
// wrapping underlying errors with it lets planner fallback branches use
// errors.Is instead of string matching.
var ErrFingerprintFailed = errors.New("fingerprint failed")
```

### 4.3 Planner shape

```go
type Planner struct {
    baseline    []BaselineLayerMeta
    readBlob    func(digest.Digest) ([]byte, error)
    fingerprint Fingerprinter                       // nil → DefaultFingerprinter{}
    baselineFP  map[digest.Digest]Fingerprint       // populated eagerly; nil entries on failure
}

func NewPlanner(
    baseline []BaselineLayerMeta,
    readBlob func(digest.Digest) ([]byte, error),
    fp Fingerprinter,
) *Planner
```

- `NewPlanner` iterates baselines, calls `readBlob`+`fingerprint`, and
  caches each result (nil on failure) into `p.baselineFP`.
- For each shipped layer in `Run`, it fingerprints the target, calls
  `pickSimilar(targetFP, targetSize)`, then proceeds with the existing
  patch-vs-full choice.
- `pickClosest` is preserved as an internal method and exercised by the
  fallback branches below.

### 4.4 `pickSimilar` decision tree

```
Input: targetFP, targetSize

1. if targetFP is nil (fingerprinting failed for target):
      → pickClosest(targetSize)           // branch A

2. Let candidates = baselines with non-nil baselineFP
   if candidates is empty:
      → pickClosest(targetSize)           // branch B

3. For each c in candidates:
      score(c) = sum over d in targetFP of:
         targetFP[d]   if d in baselineFP[c.Digest]
         else 0

4. maxScore = max(score(c))
   if maxScore == 0:
      → pickClosest(targetSize)           // branch C

5. Winners = { c | score(c) == maxScore }
   if |Winners| == 1:
      → that candidate                    // branch D

6. // Tie-break: size-closest within winners
   bestBySize = winners sorted by abs(c.Size - targetSize)
   if |bestBySize[0 ..< k]| where all have equal size-distance == 1:
      → bestBySize[0]                     // branch E

7. // Still tied: first-seen index (baselines were sorted by digest at
   // init for determinism).
      → first winner in baseline[] order  // branch F
```

Each branch corresponds to one row of the unit-test table in §8.3.

### 4.5 Fingerprinter algorithm

```
Fingerprint(ctx, mediaType, blob):
  decoder := pickDecoder(mediaType)
    switch suffix of mediaType:
      "+gzip":  r := gzip.NewReader(bytes.NewReader(blob))
      "+zstd":  r := zstd.NewReader(bytes.NewReader(blob), …)
      default:  r := bytes.NewReader(blob)            // plain tar
  tr := tar.NewReader(r)
  fp := make(Fingerprint)
  for {
      hdr := tr.Next();  if io.EOF → break;  if err → return nil, wrap(err, ErrFingerprintFailed)
      if hdr.Typeflag != tar.TypeReg: continue
      h := sha256.New()
      if _, err := io.Copy(h, tr); err ≠ nil → return nil, wrap(err, ErrFingerprintFailed)
      d := digest.NewDigest(digest.SHA256, h)
      fp[d] = hdr.Size         // dedup: same digest ⇒ same bytes ⇒ same size
  }
  return fp, nil
```

Decompressors are constructed fresh per call (layers are small enough
that reuse optimisation is premature). Decoder state errors (truncated
gzip, invalid zstd frame) bubble up through the standard error path.

### 4.6 Determinism

Two guards preserve reproducible exporter output:

1. `NewPlanner` sorts `baseline` by `Digest` before storing. Scoring
   passes iterate in that order, so tied scores resolve consistently.
2. `pickSimilar` never uses a Go map iteration for a user-visible
   ranking; tie-break cascades use the sorted `baseline` slice.

## 5. Flow

```
Export(ctx, opts)
 1. resolve baseline set (unchanged)
 2. planner := NewPlanner(baseline, readBlob, DefaultFingerprinter{})
       ├─ sort baseline by digest
       └─ for each b in baseline:
             blob, _ := readBlob(b.Digest)
             baselineFP[b.Digest], _ := fingerprint.Fingerprint(ctx, b.MediaType, blob)
             // err → baselineFP[b.Digest] = nil; log at debug verbosity
 3. for each shipped layer t:
       targetBlob := readBlob(t.Digest)
       targetFP, _ := fingerprint.Fingerprint(ctx, t.MediaType, targetBlob)
       chosen := pickSimilar(targetFP, t.Size)        // or pickClosest on fallback
       patchBytes  := zstdpatch.Encode(readBlob(chosen.Digest), targetBlob)
       fullBytes   := zstdpatch.EncodeFull(targetBlob)
       // existing min(patch, full) guard, unchanged
 4. emit sidecar + blobs (unchanged)
```

The only new I/O is the baseline fingerprint pass in step 2; downstream
flow is byte-identical to Phase 1.

## 6. Backward compatibility

- **Archive format.** No change. `patch_from_digest` still records the
  picked baseline digest; the picker algorithm is internal.
- **Sidecar schema.** No change.
- **Import path.** No change.
- **`BaselineLayerMeta`.** No field change. Fingerprints are derived at
  planner init, not stored on the meta.
- **Existing test callers** passing `nil` as the third arg to
  `NewPlanner` observe `DefaultFingerprinter{}` and — if the baselines
  happen not to be valid tars — drop silently to the size-closest path,
  exactly matching today's behaviour.

## 7. Error model

| Error | Kind | Raised by | Handling |
|---|---|---|---|
| `ErrFingerprintFailed` (new sentinel) | fingerprinter | `DefaultFingerprinter.Fingerprint` on any decoder / tar error | Planner records the baseline's FP as nil and falls through to size-only for paths that need it. Target FP failures drop directly to `pickClosest`. |
| `io.ErrUnexpectedEOF` from decompressors | fingerprinter | truncated input | Wrapped via `errors.Join(ErrFingerprintFailed, err)` so callers can distinguish cause. |
| Existing errors from Phase 1 (`ErrIntraLayerAssemblyMismatch`, `ErrBaselineMissingPatchRef`, `ErrIntraLayerUnsupported`) | unchanged | unchanged | unchanged |

No new user-visible errors. All fingerprinting failures degrade to
size-closest within the planner.

## 8. Testing strategy

### 8.1 Fingerprinter unit (`fingerprint_test.go`)

Table-driven:

| Input | Expected |
|---|---|
| Valid tar + `…+gzip` | Exact digest/size map |
| Valid tar + `…+zstd` | Same map (compression invariant) |
| Valid tar + no suffix | Same map |
| Truncated gzip | `errors.Is(err, ErrFingerprintFailed) == true` |
| Garbage bytes + any media type | Same |
| Empty tar | empty Fingerprint, nil error |
| Tar with dirs / symlinks / hardlinks only | empty Fingerprint, nil error |
| Tar with duplicate content at two paths | One entry, weight = single-instance size |
| Character device / FIFO / block device | Skipped |

### 8.2 Scoring unit (`fingerprint_test.go`)

| Input | Expected `score` |
|---|---|
| Disjoint fingerprints | 0 |
| Identical fingerprints | sum of all target sizes |
| Partial overlap (shared digest) | sum of shared-digest sizes |
| Empty target | 0 regardless of candidate |
| Nil candidate | 0 |

### 8.3 Planner decision-tree (`intralayer_test.go`)

Injectable `fakeFingerprinter` maps digests → pre-canned fingerprints or
errors. One row per branch A–F of §4.4:

| Case | Inputs | Expected branch |
|---|---|---|
| Target FP fails | target returns err | A: size-closest |
| All baseline FPs fail | target ok, all baselines err | B: size-closest |
| All scores zero | disjoint fingerprints | C: size-closest |
| Single winner | one baseline overlaps target | D: that baseline |
| Two winners tied on score, different sizes | tie-break by size | E: closer-size baseline |
| Tied score + tied size | tie-break by digest order | F: lower-digest baseline |
| Winner vs size-closest disagree | content-match has higher score than size-closest | D: content winner |

### 8.4 Fixture round-trip (`pkg/importer/integration_test.go`)

New `v4` fixture pair emitted by `scripts/build_fixtures/main.go`:

- Target layers: `T_small` (1 MB), `T_app` (50 MB), `T_data` (120 MB).
- Baseline layers:
  - `B_size_trap`: 50 MB of random bytes (size-closest to `T_app` but
    content-disjoint).
  - `B_app_v1`: 55 MB that shares 90 % of files with `T_app`.
  - `B_data_v1`: 115 MB sharing 95 % of files with `T_data`.
  - Two small decoy layers.

Integration test asserts:
1. Round-trip succeeds byte-exact.
2. The sidecar records `patch_from_digest = B_app_v1` for `T_app` and
   `B_data_v1` for `T_data` — NOT `B_size_trap`.
3. An identical export with a `SizeOnlyFingerprinter` fake (scores
   always zero, forcing branch C) produces a strictly larger archive.

### 8.5 Determinism regression

A repeat-export test runs the same inputs twice and asserts
byte-identical archive + sidecar output. Guards against regressions in
the baseline-sort / tie-break ordering.

## 9. Measurement spike (plan-level Task 0 gate)

**Purpose.** Prove content-match beats size-closest on the POC blobs
before investing implementation effort.

**Location.** `scripts/contentsim-spike/main.go`. Throwaway,
not committed long-term.

**Input.** Re-uses `/tmp/diffah-poc/*.blob` (already present from
Phase 1).

**Output.** A table printed to stdout:

```
pair                       size-closest bytes   content-match bytes    Δ      wall Δ
service-A 5.2→5.3           342 MB              ? MB                   ?      ?
service-B 5.2→5.3            19 MB              ? MB                   ?      ?
service-C 5.2→5.3           187 MB              ? MB                   ?      ?
```

**Acceptance:**
1. For every measured pair, `content-match total patch bytes ≤ size-closest bytes` (equality allowed).
2. Fingerprinting + scoring wall time adds at most 2× the size-only wall time.

**On failure:**
- If (1) fails: investigate the specific pair (path renames? tar format
  drift? compression mismatch?) and either tune the algorithm or
  abandon the feature with measurements documented in the decision
  record.
- If (2) fails: re-profile to see whether cost is in decompression
  (bounded by layer size) or SHA-256 (bounded by layer size). If it's
  intrinsic, narrow scope by adding the R2 size-pre-filter that
  §3 decision #2 deferred.

The spike's decision record is written to
`docs/superpowers/decisions/2026-04-XX-content-similarity-algorithm.md`
before Task 1 begins.

## 10. Risks

### 10.1 Spike fails — content-match not better in practice

**Severity:** medium. The bet is that tar-entry overlap correlates with
patch-from ratio. Plausible failure mode: image layers where files have
subtle byte-level differences (timestamps, inode ordering) break
digest equality but preserve zstd-window similarity. In that world,
FastCDC-chunking on decompressed bytes would beat digest-sets.

**Mitigation:** §9 gate. If the spike fails, we produce a decision
record documenting the measurement and either:
- Switch to FastCDC (new dep; separate spec),
- Keep size-closest and shelve the feature, or
- Tune the algorithm (path+content hybrid, weighted differently).

### 10.2 Eager baseline fingerprinting wall time

**Severity:** low. Baselines are read once either way. Fingerprinting
adds a decompress + SHA-256 pass. On SSD, decompress is ~500 MB/s
(gzip) / ~1 GB/s (zstd), SHA-256 is ~1 GB/s — so a 10×200 MB baseline
costs ~5 s. If a user has pathological 20 × 2 GB baselines, setup could
stretch to minutes.

**Mitigation:** acceptable for Phase 2 MVP. Lazy fingerprinting + size
pre-filter is a clean follow-up if complaints surface.

### 10.3 Non-tar foreign blobs (rare OCI configs referenced as layers)

**Severity:** low. Fingerprinter returns `ErrFingerprintFailed`;
planner logs at debug verbosity and records nil. Size-closest still
works. No user-visible regression.

### 10.4 Determinism slip

**Severity:** medium. Archive reproducibility matters for both CI and
for operators auditing deltas. A missed `sort` could leak map
randomisation into output.

**Mitigation:** §8.5 regression test (repeat-export byte-equality) is
the canary.

### 10.5 Bundled dependencies on klauspost/compress

**Severity:** low. `klauspost/compress/zstd` is already a direct
dependency (see `go.mod`). The `+zstd` branch in the fingerprinter
constructs its own `zstd.NewReader`; no coordination with the
backend-resilience spec is required.

## 11. Rollout

- Single patch. Additive only — behaviour change is strictly
  "sometimes picks a better baseline," never worse.
- Feature flag: none. If a regression escapes, mitigation is to revert
  and re-run the spike with the adverse input.
- README change: one sentence to the "How it picks baselines" section,
  pointing at the new matcher.

## 12. Open questions (deferred to plan)

1. Debug logging verbosity for fingerprint failures — `slog.Debug` seems
   right; nail down the attributes in the plan.
2. Whether to expose a `--debug-matching` flag that prints the chosen
   baseline + score per shipped layer. Defer; user hasn't asked.
3. Interaction with spec II.c (cross-image patching): the
   `Fingerprinter` and `Fingerprint` types are the natural shared
   primitive. Confirm the package boundary when II.c is specced — we
   may want to hoist them into a `pkg/layermatch` package at that
   point.
