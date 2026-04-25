# Importer baseline-blob dedup — design

**Date:** 2026-04-25
**Status:** Spec
**Tracking:** Phase 2 Goal 4 regression (export side dedup'd via `pkg/exporter/fpcache.go`; import side never got the equivalent). Queue 1 of [`phase234_review_findings_2026-04-25.md`](../../../.claude/projects/-Users-leosocy-workspace-repos-myself-diffah/memory/phase234_review_findings_2026-04-25.md).

## 1. Problem

`pkg/importer/compose.go::fetchVerifiedBaselineBlob` calls `s.baseline.GetBlob` directly on every miss. It is invoked from two paths:

- `compose.go::GetBlob` (sidecar miss — layer not shipped, must come from baseline)
- `compose.go::servePatch` (patch-from reference — baseline blob needed to decode the patch)

The same baseline blob digest can be requested multiple times within a single `Import()` call:

1. **Within one image.** A baseline layer that is referenced by `PatchFromDigest` for one shipped patch *and* appears as a baseline-only layer in the target manifest at another position is fetched twice.
2. **Across images.** Two images in the same multi-image bundle that share a baseline blob digest (e.g. both based on `alpine:3.18`) each fetch that digest from the registry.

In addition, `pkg/importer/compose.go:63` declares `HasThreadSafeGetBlob() bool { return false }`, which forces `copy.Image` to serialize all `GetBlob` calls. Combined with the re-fetch behavior above, the apply path is single-threaded **and** re-fetching identical blobs over WAN.

The export side already solved this in `pkg/exporter/fpcache.go` — `singleflight.Group` + `sync.RWMutex` + a digest→bytes map, ensuring each baseline blob is fetched at most once per `Export()`. The import side did not get the equivalent.

## 2. Goals

1. Fetch every distinct baseline blob digest **at most once** per `Import()` call, regardless of how many shipped patches reference it or how many images in the bundle share it.
2. Allow `copy.Image` to copy layers in parallel when the underlying baseline source is thread-safe (i.e., `docker://` baselines).
3. Verify each baseline blob's digest before populating the cache; reject mismatches as today via `diff.ErrBaselineBlobDigestMismatch`.
4. No public API changes; no on-disk format changes; no CLI flag changes.

## 3. Non-goals

- Persistent cross-run blob cache (`~/.cache/diffah/`) — Phase 5+ per the Phase 4 design doc.
- Streaming `io.Reader`-based blob path — Phase 6+ per the Phase 4 design doc.
- Tightening `cmd/bandwidth_integration_test.go` ≤10 slack on the `diff` (export) side — separate Queue 2 G3 follow-up.
- `Phase 4 G4` no-TMPDIR-materialization assertion — separate Queue 2 follow-up.

## 4. Design

### 4.1 Architecture

```
Import(ctx, opts)
  ├─ extractBundle()
  ├─ verifySignature()                              [unchanged]
  ├─ resolveBaselines()                             [unchanged]
  ├─ cache := newBaselineBlobCache()                ← NEW: one per Import()
  └─ importEachImage(ctx, bundle, ..., cache)
        └─ for each img:
              composeImage(ctx, img, ..., cache)    ← NEW param
                 └─ src := &bundleImageSource{... cache: cache}
                 └─ copy.Image(...)
                       calls src.GetBlob(d) concurrently
                              ↓
                       (sidecar miss OR patch-from path)
                              ↓
                       fetchVerifiedBaselineBlob(ctx, d, ...)
                              ↓
                       cache.GetOrLoad(ctx, d, fetch)
```

### 4.2 Cache lifetime, key, value

- **Lifetime:** allocated at the top of `Import()`, lives until `Import()` returns. Garbage-collected with `bundle` and resolved baselines.
- **Key:** `digest.Digest` only. Justified because digests are content-addressable: identical digests across different baseline sources mean identical bytes.
- **Value:** verified `[]byte`. Verification is performed *inside* the singleflight closure, so concurrent waiters all see verified bytes; a verification failure does not poison a cache entry.

### 4.3 Cache type — `pkg/importer/blobcache.go`

New file, ~50 LOC, modeled on `pkg/exporter/fpcache.go`:

```go
package importer

import (
    "context"
    "sync"

    "github.com/opencontainers/go-digest"
    "golang.org/x/sync/singleflight"
)

// baselineBlobCache memoizes verified baseline blob bytes across all
// images in a single Import() call. Concurrent misses on the same
// digest collapse to one underlying fetch via singleflight; fetch or
// verify errors are NOT cached — the next caller retries.
type baselineBlobCache struct {
    mu    sync.RWMutex
    bytes map[digest.Digest][]byte
    sf    singleflight.Group
}

func newBaselineBlobCache() *baselineBlobCache {
    return &baselineBlobCache{bytes: make(map[digest.Digest][]byte)}
}

// GetOrLoad returns verified bytes for d. On cache miss it calls fetch
// exactly once even under concurrent callers; on fetch error nothing
// is cached.
func (c *baselineBlobCache) GetOrLoad(
    ctx context.Context, d digest.Digest, fetch func() ([]byte, error),
) ([]byte, error) {
    if b, ok := c.lookup(d); ok {
        return b, nil
    }
    v, err, _ := c.sf.Do(string(d), func() (any, error) {
        if b, ok := c.lookup(d); ok {
            return b, nil
        }
        data, err := fetch()
        if err != nil {
            return nil, err
        }
        c.mu.Lock()
        c.bytes[d] = data
        c.mu.Unlock()
        return data, nil
    })
    if err != nil {
        return nil, err
    }
    return v.([]byte), nil
}

func (c *baselineBlobCache) lookup(d digest.Digest) ([]byte, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    b, ok := c.bytes[d]
    return b, ok
}
```

The `ctx` parameter is forwarded for symmetry with `fpCache.GetOrLoad` and to allow future cancellation propagation; today the singleflight closure does not consult it directly.

### 4.4 Changes to `pkg/importer/compose.go`

```go
type bundleImageSource struct {
    // ... existing fields
    cache *baselineBlobCache  // NEW; non-nil; per-Import shared
}

func (s *bundleImageSource) HasThreadSafeGetBlob() bool {
    return s.baseline.HasThreadSafeGetBlob()  // was: return false
}

func (s *bundleImageSource) fetchVerifiedBaselineBlob(
    ctx context.Context, d digest.Digest, cache types.BlobInfoCache,
) ([]byte, error) {
    return s.cache.GetOrLoad(ctx, d, func() ([]byte, error) {
        rc, _, err := s.baseline.GetBlob(ctx, types.BlobInfo{Digest: d}, cache)
        if err != nil {
            return nil, err
        }
        defer rc.Close()
        data, err := io.ReadAll(rc)
        if err != nil {
            return nil, err
        }
        if got := digest.FromBytes(data); got != d {
            return nil, &diff.ErrBaselineBlobDigestMismatch{
                ImageName: s.imageName, Digest: d.String(), Got: got.String(),
            }
        }
        return data, nil
    })
}

func composeImage(
    ctx context.Context, img diff.ImageEntry, bundle *extractedBundle,
    rb resolvedBaseline, destRef types.ImageReference, sysctx *types.SystemContext,
    allowConvert bool, _ progress.Reporter,
    cache *baselineBlobCache,  // NEW
) error { /* ... */ src := &bundleImageSource{ /* ... */ cache: cache } /* ... */ }
```

### 4.5 Changes to `pkg/importer/importer.go`

```go
func Import(ctx context.Context, opts Options) error {
    // ... existing setup unchanged through resolveBaselines() ...
    cache := newBaselineBlobCache()
    imported, skipped, err := importEachImage(
        ctx, bundle, resolvedByName, outputs, opts, cache)
    // ... rest unchanged
}

func importEachImage(
    ctx context.Context, bundle *extractedBundle,
    resolvedByName map[string]resolvedBaseline,
    outputs map[string]string, opts Options,
    cache *baselineBlobCache,  // NEW
) (int, []string, error) {
    // ... existing loop, calls composeImage(..., cache)
}
```

`Import` is the only public entry. Internal helpers gain a `cache` parameter; no public API change.

`DryRun` is unchanged — it does not open `bundleImageSource` and does not call `GetBlob`, so it does not need the cache.

### 4.6 `HasThreadSafeGetBlob` propagation

The change `return s.baseline.HasThreadSafeGetBlob()` (rather than unconditional `true`) is deliberate:

| Baseline transport | Upstream `HasThreadSafeGetBlob` | Apply parallelism after fix |
|---|---|---|
| `docker://` (registry) | `true` | parallel layer copy |
| `oci-archive:` | `false` | serial (safe) |
| `openshift:` | `false` | serial (safe) |
| `docker-archive:` | delegates to `internal` | follows internal claim |
| `dir:` | `true` | parallel layer copy |

Mirroring the underlying claim gives parallelism for the spec's primary case (registry-native import) without breaking archive baselines.

The cache itself is concurrent-safe regardless: `singleflight` collapses same-digest concurrent calls to one upstream `GetBlob`; distinct-digest concurrent calls fan out to the underlying source, which is the source's own concern.

## 5. Testing

### 5.1 Unit — `pkg/importer/blobcache_test.go`

Mirrors `pkg/exporter/fpcache_test.go`:

| Test | Assertion |
|---|---|
| `TestBlobCache_FirstFetchMisses` | First `GetOrLoad(d)` invokes fetch, returns bytes |
| `TestBlobCache_SecondFetchHits` | Second `GetOrLoad(d)` returns cached bytes; fetch counter unchanged |
| `TestBlobCache_ConcurrentMissesCollapse` | 100 goroutines on same digest → fetch called exactly 1× |
| `TestBlobCache_ConcurrentDistinctDigests` | 10 goroutines on 10 distinct digests → 10 fetches; per-digest results correct |
| `TestBlobCache_FetchErrorNotCached` | First call errors; second call retries; fetch called 2× |

Run with `-race` to catch any latent data race.

### 5.2 Integration — `cmd/apply_registry_integration_test.go`

Two new tests under the existing `//go:build integration` tag.

**`TestApplyCLI_BaselineBlobsFetchedExactlyOnce_SingleImage`:**

1. Spin up `registrytest.New(t)`.
2. Push `testdata/fixtures/v1_oci.tar` into `app/v1` repo.
3. Build delta `oci-archive:v1_oci → oci-archive:v2_oci → tmp/delta.tar` via the diffah binary (matches existing pattern in `buildDelta` helper).
4. Snapshot `before := len(srv.BlobHits())`.
5. Run `diffah apply tmp/delta.tar docker://reg/app/v1 oci-archive:tmp/restored.tar --tls-verify=false`.
6. Build `hits map[digest.Digest]int{}`; for each `BlobRequest` after `before` whose `Repo == "app/v1"`, increment `hits[h.Digest]`.
7. Assert `len(hits) > 0` (sanity — fixture must exercise some baseline fetch path; otherwise test trivially green).
8. Assert `hits[d] == 1` for every `d` in `hits` — *this is the regression gate*.

**`TestApplyCLI_BaselineBlobsFetchedExactlyOnce_MultiImage`:**

1. Spin up `registrytest.New(t)`.
2. Push `v1_oci.tar` to **two** repos: `app-a/v1` and `app-b/v1`. Same content → identical digest set in two registry locations.
3. Build a multi-image delta with two pairs:
   - Pair `app-a`: baseline `oci-archive:v1_oci.tar`, target `oci-archive:v2_oci.tar`
   - Pair `app-b`: baseline `oci-archive:v1_oci.tar`, target `oci-archive:v2_oci.tar`
   (Built via a Go-level call to `exporter.Export` since the CLI's `diff` command builds single-pair deltas — pattern already used by `pkg/importer/registry_integration_test.go`.)
4. Snapshot `before`.
5. Run `diffah apply` with `BASELINE-SPEC` mapping `app-a=docker://reg/app-a/v1, app-b=docker://reg/app-b/v1` and corresponding `OUTPUT-SPEC`.
6. Group `BlobHits()[before:]` by `Digest`, summed across **both** baseline repos.
7. Assert each distinct baseline digest's total cross-repo hit count == 1.

   The cache picks whichever source wins the singleflight; the other repo sees zero hits for that digest. That is correct: digest is content-addressable, so the bytes are interchangeable, and the goal is "exactly one registry GET per distinct digest, period".

**Multi-image fixture caveat.** If `pkg/importer/resolve.go::resolveBaselines` happens to dedupe baseline `ImageSource` instances by ref string before this test runs, the multi-image test would already pass without the cache and not catch a per-source regression. The two distinct repos / two distinct refs setup forces two independent `types.ImageSource` instances and proves the dedup is the cache's, not the resolver's. Verify this property in `resolve.go` while writing the test; add a comment if relevant.

### 5.3 Tests that stay as-is

- `pkg/importer/registry_integration_test.go::TestImporter_LazyBaselineFetch_OnlyReferencedBlobsPulled` — orthogonal assertion ("shipped blobs are never pulled from baseline"); still valid.
- `cmd/bandwidth_integration_test.go` — `diff` (export) path; out of scope per §3.

## 6. Risks & mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| `copy.Image` + parallel `GetBlob` on `docker://` baseline races | Low | Upstream claims thread-safe; `bundleImageSource.GetBlob` uses no shared mutable state (sidecar map read-only after construction; bundle file reads independent); the cache is concurrent-safe; verified by `-race` runs in CI |
| Cache holds full baseline blob bytes — RSS pressure on big images | Medium | Bounded by `Σ(unique baseline blob sizes)` per `Import()`. Same memory profile as today on first-fetch; only difference is bytes are retained between calls instead of GC'd. Acceptable until Phase 6 streaming refactor (already on roadmap) |
| Multi-image fixture failure if `resolveBaselines` dedupes by ref string | Low | Use two distinct repos / two distinct refs; verify resolver behavior while writing the test |
| `HasThreadSafeGetBlob=true` claimed when underlying is `false` | None | We delegate to `s.baseline.HasThreadSafeGetBlob()`; never claim independently |
| Cache poisoning via wrong-digest payload | None | Verify-before-cache: bytes only enter the map after `digest.FromBytes(data) == d` |

## 7. Rollout

### 7.1 Commit sequence

1. **`feat(importer): add baselineBlobCache type`**
   New file `pkg/importer/blobcache.go` + `blobcache_test.go`. Standalone, no behavior change. Test-first: write the five unit tests red→green.

2. **`feat(importer): dedupe baseline blob fetches across all images in a bundle`**
   Wire `cache` through `Import → importEachImage → composeImage → bundleImageSource`; flip `HasThreadSafeGetBlob` to delegate. Add the two integration tests in `cmd/apply_registry_integration_test.go`. Test-first within this commit (write the failing single-image assertion, wire the cache, then add the multi-image assertion).

3. **`docs: CHANGELOG entry under Unreleased`**
   Note Phase 2 G4 regression closed; per-Import baseline blob dedup; parallel apply for `docker://` baselines.

Three commits, none over ~200 LOC.

### 7.2 Reversibility

Pure code-level addition in the import path. Single-commit revert if problems surface. No on-disk format change, no CLI flag change, no public API change.

### 7.3 Behavior change observable to users

- Apply against `docker://` baselines:
  - Fewer registry GETs (one per distinct baseline digest, regardless of fan-out).
  - Faster wall-clock for multi-layer images (`copy.Image` parallelizes).
- Apply against `oci-archive:` / `openshift:` / `docker-archive:` baselines:
  - Same parallelism characteristics as today (serial `copy.Image`, since underlying source claims `false`).
  - Still benefits from per-`Import` blob dedup if any layer is referenced more than once.
