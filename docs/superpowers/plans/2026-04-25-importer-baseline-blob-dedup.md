# Importer baseline-blob dedup — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore Phase 2 Goal 4 — every distinct baseline blob digest is fetched at most once per `Import()` call (regardless of how many shipped patches reference it or how many images in the bundle share it) — and unlock parallel layer copy when the underlying baseline supports it.

**Architecture:** Add a per-`Import()` `baselineBlobCache` (`pkg/importer/blobcache.go`, ~50 LOC) modelled on `pkg/exporter/fpcache.go`: `singleflight.Group` + `sync.RWMutex` + `map[digest.Digest][]byte`. Thread it through `Import → importEachImage → composeImage → bundleImageSource`. Verify-before-cache. Flip `bundleImageSource.HasThreadSafeGetBlob()` from hard-coded `false` to delegate to the underlying baseline source. Verify with one unit-test file and two CLI-level integration tests (single-image in `cmd/apply_registry_integration_test.go`, multi-image in a new `cmd/unbundle_registry_integration_test.go`).

**Tech Stack:** Go 1.25+, `golang.org/x/sync/singleflight` (already in go.mod), `github.com/opencontainers/go-digest`, `go.podman.io/image/v5/types`, `internal/registrytest` (existing harness with `BlobHits`), `pkg/exporter` (called Go-level from the multi-image test).

**Spec:** `docs/superpowers/specs/2026-04-25-importer-baseline-blob-dedup-design.md` (commit `d14ee03`).

**Branch / worktree:** `feat/importer-baseline-blob-dedup` at `.worktrees/baseline-blob-dedup`. All steps below run from that worktree's repo root.

**Spec deviation note:** Spec §5.2 places both integration tests in `cmd/apply_registry_integration_test.go`. The multi-image scenario is implemented via `diffah unbundle` (the multi-image apply CLI) rather than `diffah apply` (single-image only), so the multi-image test moves to a new `cmd/unbundle_registry_integration_test.go` for file/command consistency. The spec's intent (a CLI-level cross-image dedup assertion) is preserved.

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `pkg/importer/blobcache.go` | **Create** | Per-`Import()` cache type: `baselineBlobCache`, `newBaselineBlobCache`, `GetOrLoad`, `lookup`. ~50 LOC. |
| `pkg/importer/blobcache_test.go` | **Create** | Five unit tests mirroring `pkg/exporter/fpcache_test.go`. |
| `pkg/importer/compose.go` | **Modify** | Add `cache *baselineBlobCache` to `bundleImageSource`; change `HasThreadSafeGetBlob` to delegate; rewrite `fetchVerifiedBaselineBlob` to go through cache; add `cache` parameter to `composeImage`. |
| `pkg/importer/importer.go` | **Modify** | Construct `cache := newBaselineBlobCache()` inside `Import`; thread it into `importEachImage` and `composeImage`. |
| `cmd/apply_registry_integration_test.go` | **Modify** | Add one test: `TestApplyCLI_BaselineBlobsFetchedExactlyOnce_SingleImage`. |
| `cmd/unbundle_registry_integration_test.go` | **Create** | Add one test: `TestUnbundleCLI_BaselineBlobsFetchedExactlyOnce_MultiImage`. |
| `CHANGELOG.md` | **Modify** | Add bullet under `[Unreleased] → Internal` describing dedup + parallel apply. |

---

## Phase 1 — `baselineBlobCache` type

Build the cache as a self-contained, behavior-neutral unit. No wiring yet.

### Task 1.1: Write the failing unit tests

**Files:**
- Create: `pkg/importer/blobcache_test.go`

- [ ] **Step 1: Write the test file**

Create `pkg/importer/blobcache_test.go` with the following content:

```go
package importer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestBlobCache_FirstFetchMisses_SecondHits(t *testing.T) {
	c := newBaselineBlobCache()
	d := digest.Digest("sha256:" + strings.Repeat("a", 64))
	want := []byte("hello")

	var calls atomic.Int64
	fetch := func() ([]byte, error) {
		calls.Add(1)
		return want, nil
	}

	got1, err := c.GetOrLoad(context.Background(), d, fetch)
	if err != nil {
		t.Fatalf("first GetOrLoad: %v", err)
	}
	if string(got1) != "hello" {
		t.Fatalf("first GetOrLoad bytes: got %q want %q", got1, "hello")
	}
	got2, err := c.GetOrLoad(context.Background(), d, fetch)
	if err != nil {
		t.Fatalf("second GetOrLoad: %v", err)
	}
	if string(got2) != "hello" {
		t.Fatalf("second GetOrLoad bytes: got %q want %q", got2, "hello")
	}
	if calls.Load() != 1 {
		t.Fatalf("fetch invoked %d times across two calls, want 1", calls.Load())
	}
}

func TestBlobCache_ConcurrentMissesCollapseToOneFetch(t *testing.T) {
	c := newBaselineBlobCache()
	d := digest.Digest("sha256:" + strings.Repeat("b", 64))

	var calls atomic.Int64
	// Block briefly so concurrent goroutines all reach c.sf.Do before
	// the singleflight winner returns; without this the loop is fast
	// enough that the first goroutine completes before the rest enter.
	gate := make(chan struct{})
	fetch := func() ([]byte, error) {
		<-gate
		calls.Add(1)
		return []byte("x"), nil
	}

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.GetOrLoad(context.Background(), d, fetch)
		}()
	}
	close(gate)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fetch invoked %d times under singleflight, want 1", calls.Load())
	}
}

func TestBlobCache_ConcurrentDistinctDigests(t *testing.T) {
	c := newBaselineBlobCache()
	const N = 10
	digests := make([]digest.Digest, N)
	for i := 0; i < N; i++ {
		digests[i] = digest.Digest("sha256:" + strings.Repeat("0123456789abcdef"[i:i+1], 64))
	}

	var calls atomic.Int64
	fetch := func(d digest.Digest) func() ([]byte, error) {
		return func() ([]byte, error) {
			calls.Add(1)
			return []byte(d.Encoded()[:8]), nil
		}
	}

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			b, err := c.GetOrLoad(context.Background(), digests[i], fetch(digests[i]))
			if err != nil {
				t.Errorf("digest %d: %v", i, err)
				return
			}
			if string(b) != digests[i].Encoded()[:8] {
				t.Errorf("digest %d: bytes %q, want %q", i, b, digests[i].Encoded()[:8])
			}
		}()
	}
	wg.Wait()
	if calls.Load() != N {
		t.Fatalf("fetch invoked %d times for %d distinct digests, want %d", calls.Load(), N, N)
	}
}

func TestBlobCache_FetchErrorNotCached(t *testing.T) {
	c := newBaselineBlobCache()
	d := digest.Digest("sha256:" + strings.Repeat("c", 64))
	want := errors.New("transient")

	var calls atomic.Int64
	fetch := func() ([]byte, error) {
		n := calls.Add(1)
		if n == 1 {
			return nil, want
		}
		return []byte("ok"), nil
	}

	if _, err := c.GetOrLoad(context.Background(), d, fetch); !errors.Is(err, want) {
		t.Fatalf("first call: got %v, want %v", err, want)
	}
	got, err := c.GetOrLoad(context.Background(), d, fetch)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("second call bytes: got %q, want %q", got, "ok")
	}
	if calls.Load() != 2 {
		t.Fatalf("fetch invoked %d times, want 2 (no error caching)", calls.Load())
	}
}

func TestBlobCache_FetchErrorOnConcurrentMissReturnsToAllWaiters(t *testing.T) {
	c := newBaselineBlobCache()
	d := digest.Digest("sha256:" + strings.Repeat("d", 64))
	want := errors.New("upstream-down")

	gate := make(chan struct{})
	var calls atomic.Int64
	fetch := func() ([]byte, error) {
		<-gate
		calls.Add(1)
		return nil, want
	}

	const N = 32
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			_, errs[i] = c.GetOrLoad(context.Background(), d, fetch)
		}()
	}
	close(gate)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fetch invoked %d times under singleflight, want 1", calls.Load())
	}
	for i, err := range errs {
		if !errors.Is(err, want) {
			t.Fatalf("waiter %d got err %v, want %v", i, err, want)
		}
	}
}
```

- [ ] **Step 2: Run the tests; expect compile failure**

```bash
cd .worktrees/baseline-blob-dedup
go test ./pkg/importer/... -run BlobCache -count=1
```

Expected output (compile error — `baselineBlobCache` and `newBaselineBlobCache` undefined):

```
pkg/importer/blobcache_test.go:NN:NN: undefined: newBaselineBlobCache
FAIL    github.com/leosocy/diffah/pkg/importer [build failed]
```

This is the RED state.

---

### Task 1.2: Implement `baselineBlobCache`

**Files:**
- Create: `pkg/importer/blobcache.go`

- [ ] **Step 1: Write the implementation**

Create `pkg/importer/blobcache.go`:

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

The `ctx` parameter is forwarded for symmetry with `pkg/exporter/fpcache.GetOrLoad` and for future cancellation propagation; today the closure doesn't consult it directly.

- [ ] **Step 2: Run the tests; expect PASS**

```bash
go test ./pkg/importer/... -run BlobCache -count=1 -v
```

Expected output:

```
=== RUN   TestBlobCache_FirstFetchMisses_SecondHits
--- PASS: TestBlobCache_FirstFetchMisses_SecondHits
=== RUN   TestBlobCache_ConcurrentMissesCollapseToOneFetch
--- PASS: TestBlobCache_ConcurrentMissesCollapseToOneFetch
=== RUN   TestBlobCache_ConcurrentDistinctDigests
--- PASS: TestBlobCache_ConcurrentDistinctDigests
=== RUN   TestBlobCache_FetchErrorNotCached
--- PASS: TestBlobCache_FetchErrorNotCached
=== RUN   TestBlobCache_FetchErrorOnConcurrentMissReturnsToAllWaiters
--- PASS: TestBlobCache_FetchErrorOnConcurrentMissReturnsToAllWaiters
PASS
```

- [ ] **Step 3: Run with race detector**

```bash
go test ./pkg/importer/... -run BlobCache -count=1 -race
```

Expected: `PASS` with no `WARNING: DATA RACE` lines.

---

### Task 1.3: Lint & gofmt

**Files:** none new.

- [ ] **Step 1: Format**

```bash
gofmt -w pkg/importer/blobcache.go pkg/importer/blobcache_test.go
```

Expected: no output, no diff.

- [ ] **Step 2: Vet**

```bash
go vet ./pkg/importer/...
```

Expected: no output.

- [ ] **Step 3: Lint (if available)**

```bash
which golangci-lint && golangci-lint run pkg/importer/blobcache.go pkg/importer/blobcache_test.go
```

If `golangci-lint` is not installed, skip. Otherwise expected: no findings.

---

### Task 1.4: Commit Phase 1

**Files:** stage only the two new files; do NOT stage anything else.

- [ ] **Step 1: Stage**

```bash
git add pkg/importer/blobcache.go pkg/importer/blobcache_test.go
git status -s
```

Expected:

```
A  pkg/importer/blobcache.go
A  pkg/importer/blobcache_test.go
```

- [ ] **Step 2: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(importer): add baselineBlobCache type

Self-contained per-Import() cache that memoizes verified baseline
blob bytes keyed on digest. singleflight collapses concurrent misses
on the same key; fetch or verify errors are not cached.

Models pkg/exporter/fpcache.go but trimmed to a digest→bytes map
(no fingerprint slot — that concern is exporter-only).

No callers yet; wiring lands in the next commit.

Refs: docs/superpowers/specs/2026-04-25-importer-baseline-blob-dedup-design.md §4.3
EOF
)"
git log -1 --format='%h %s'
```

Expected: a new commit hash, subject `feat(importer): add baselineBlobCache type`.

---

## Phase 2 — Wire cache & integration tests

### Task 2.1: Add the multi-image RED integration test

This test is **guaranteed** to fail today: two `Pair`s share baseline content; without dedup, `BlobHits` show each digest fetched twice across the two baseline repos.

**Files:**
- Create: `cmd/unbundle_registry_integration_test.go`

- [ ] **Step 1: Write the test file**

Create `cmd/unbundle_registry_integration_test.go`:

```go
//go:build integration

package cmd_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

// TestUnbundleCLI_BaselineBlobsFetchedExactlyOnce_MultiImage builds
// a two-pair bundle whose pairs share identical baseline content
// (same fixture pushed to two distinct registry repos, app-a/v1 and
// app-b/v1). After diffah unbundle, every distinct baseline blob
// digest must appear in BlobHits exactly once total across both
// repos — proving the per-Import baselineBlobCache deduplicates
// fetches across images. Without the cache this assertion fails
// because each bundleImageSource opens an independent ImageSource
// and re-fetches every required blob.
func TestUnbundleCLI_BaselineBlobsFetchedExactlyOnce_MultiImage(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)

	// Same fixture, two repos → two independent ImageSource instances
	// during apply, identical digest sets. The cache must collapse
	// duplicate-digest fetches across both sources.
	v1 := filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	v2 := filepath.Join(root, "testdata/fixtures/v2_oci.tar")
	seedOCIIntoRegistry(t, srv, "app-a/v1", v1, nil)
	seedOCIIntoRegistry(t, srv, "app-b/v1", v1, nil)

	tmp := t.TempDir()

	// 1) Build the multi-image bundle JSON for `diffah bundle`.
	bundleSpec := map[string]any{
		"pairs": []map[string]string{
			{"name": "app-a", "baseline": "oci-archive:" + v1, "target": "oci-archive:" + v2},
			{"name": "app-b", "baseline": "oci-archive:" + v1, "target": "oci-archive:" + v2},
		},
	}
	bundleSpecPath := filepath.Join(tmp, "bundle.json")
	raw, err := json.Marshal(bundleSpec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(bundleSpecPath, raw, 0o600))

	bundleOut := filepath.Join(tmp, "bundle.tar")
	cmd := exec.Command(bin, "bundle", bundleSpecPath, bundleOut)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "bundle failed: %s", string(out))

	// 2) baseline-spec maps each pair name to its registry repo.
	baselineSpec := map[string]any{
		"baselines": map[string]string{
			"app-a": registryDockerURL(t, srv, "app-a/v1"),
			"app-b": registryDockerURL(t, srv, "app-b/v1"),
		},
	}
	baselineSpecPath := filepath.Join(tmp, "baselines.json")
	raw, err = json.Marshal(baselineSpec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(baselineSpecPath, raw, 0o600))

	// 3) outputs map each pair to an oci-archive sink.
	outputsSpec := map[string]any{
		"outputs": map[string]string{
			"app-a": "oci-archive:" + filepath.Join(tmp, "restored-a.tar"),
			"app-b": "oci-archive:" + filepath.Join(tmp, "restored-b.tar"),
		},
	}
	outputsPath := filepath.Join(tmp, "outputs.json")
	raw, err = json.Marshal(outputsSpec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(outputsPath, raw, 0o600))

	// 4) Snapshot BlobHits before apply.
	before := len(srv.BlobHits())

	// 5) Run `diffah unbundle` against the two registry baselines.
	_, stderr, exit := runDiffahBin(t, bin,
		"unbundle", bundleOut, baselineSpecPath, outputsPath,
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "unbundle failed: %s", stderr)

	// 6) Group hits in either baseline repo by digest, summed across
	//    both repos. The cache picks whichever source wins the
	//    singleflight; the other repo sees zero hits for that digest.
	//    The contract is "exactly one registry GET per distinct
	//    digest, period" — across both repos.
	totalPerDigest := make(map[digest.Digest]int)
	all := srv.BlobHits()
	require.Greater(t, len(all), before, "registry must have observed at least one blob fetch")
	for _, h := range all[before:] {
		switch h.Repo {
		case "app-a/v1", "app-b/v1":
			totalPerDigest[h.Digest]++
		}
	}
	require.NotEmptyf(t, totalPerDigest,
		"expected at least one baseline-blob fetch across app-a/v1 and app-b/v1 — fixture must exercise the baseline-fetch path")
	for d, n := range totalPerDigest {
		t.Logf("baseline blob %s total cross-repo hits=%d", d, n)
		require.Equalf(t, 1, n,
			"baseline blob %s fetched %d times across app-a/v1 + app-b/v1; want exactly 1 — dedup regression", d, n)
	}

	// Sanity: outputs should both exist and be non-empty.
	for _, name := range []string{"restored-a.tar", "restored-b.tar"} {
		info, err := os.Stat(filepath.Join(tmp, name))
		require.NoError(t, err, "missing output %s", name)
		require.Greater(t, info.Size(), int64(0))
	}
}
```

- [ ] **Step 2: Run the test; expect FAIL**

```bash
go test -tags integration ./cmd/... -run TestUnbundleCLI_BaselineBlobsFetchedExactlyOnce_MultiImage -count=1 -v
```

Expected: a `FAIL` line of the form

```
unbundle_registry_integration_test.go:NN:
    Error: ... baseline blob sha256:... fetched 2 times across app-a/v1 + app-b/v1; want exactly 1 — dedup regression
```

This is the RED gate the cache will turn green.

---

### Task 2.2: Wire the cache into `bundleImageSource`

**Files:**
- Modify: `pkg/importer/compose.go:41-49` (struct fields), `:63` (HasThreadSafeGetBlob), `:138-156` (fetchVerifiedBaselineBlob), `:200-253` (composeImage signature + src construction)

- [ ] **Step 1: Add `cache` field to `bundleImageSource`**

In `pkg/importer/compose.go`, change the struct from:

```go
type bundleImageSource struct {
	blobDir      string
	manifest     []byte
	manifestMime string
	sidecar      *diff.Sidecar
	baseline     types.ImageSource
	imageName    string
	ref          types.ImageReference
}
```

to:

```go
type bundleImageSource struct {
	blobDir      string
	manifest     []byte
	manifestMime string
	sidecar      *diff.Sidecar
	baseline     types.ImageSource
	imageName    string
	ref          types.ImageReference
	cache        *baselineBlobCache
}
```

- [ ] **Step 2: Delegate `HasThreadSafeGetBlob`**

Change:

```go
func (s *bundleImageSource) HasThreadSafeGetBlob() bool { return false }
```

to:

```go
func (s *bundleImageSource) HasThreadSafeGetBlob() bool {
	return s.baseline.HasThreadSafeGetBlob()
}
```

- [ ] **Step 3: Route `fetchVerifiedBaselineBlob` through the cache**

Change:

```go
func (s *bundleImageSource) fetchVerifiedBaselineBlob(
	ctx context.Context, d digest.Digest, cache types.BlobInfoCache,
) ([]byte, error) {
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
}
```

to:

```go
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
```

The `cache types.BlobInfoCache` parameter is the upstream `containers-image` blob info cache (separate from our `s.cache`); it must remain in the signature and be forwarded to `s.baseline.GetBlob`.

- [ ] **Step 4: Add `cache` parameter to `composeImage` and pass it into `src`**

Change `composeImage`'s signature in `pkg/importer/compose.go` from:

```go
func composeImage(
	ctx context.Context,
	img diff.ImageEntry,
	bundle *extractedBundle,
	rb resolvedBaseline,
	destRef types.ImageReference,
	sysctx *types.SystemContext,
	allowConvert bool,
	_ progress.Reporter, // hook site for per-blob pull/push progress; not yet wired
) error {
```

to:

```go
func composeImage(
	ctx context.Context,
	img diff.ImageEntry,
	bundle *extractedBundle,
	rb resolvedBaseline,
	destRef types.ImageReference,
	sysctx *types.SystemContext,
	allowConvert bool,
	_ progress.Reporter, // hook site for per-blob pull/push progress; not yet wired
	cache *baselineBlobCache,
) error {
```

In the same function, change the `src` literal from:

```go
	src := &bundleImageSource{
		blobDir:      bundle.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      bundle.sidecar,
		baseline:     rb.Src, // already open — DO NOT open a fresh one
		imageName:    img.Name,
	}
```

to:

```go
	src := &bundleImageSource{
		blobDir:      bundle.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      bundle.sidecar,
		baseline:     rb.Src, // already open — DO NOT open a fresh one
		imageName:    img.Name,
		cache:        cache,
	}
```

- [ ] **Step 5: Build to surface compile errors in `importer.go` callers**

```bash
go build ./pkg/importer/...
```

Expected: a compile error in `pkg/importer/importer.go` because `composeImage` is called without a `cache` argument. That tells us where to wire the cache next.

---

### Task 2.3: Wire the cache through `Import` and `importEachImage`

**Files:**
- Modify: `pkg/importer/importer.go:104` (`Import`), `:119-153` (`importEachImage`)

- [ ] **Step 1: Add cache parameter to `importEachImage`**

In `pkg/importer/importer.go`, change `importEachImage`'s signature from:

```go
func importEachImage(
	ctx context.Context,
	bundle *extractedBundle,
	resolvedByName map[string]resolvedBaseline,
	outputs map[string]string,
	opts Options,
) (int, []string, error) {
```

to:

```go
func importEachImage(
	ctx context.Context,
	bundle *extractedBundle,
	resolvedByName map[string]resolvedBaseline,
	outputs map[string]string,
	opts Options,
	cache *baselineBlobCache,
) (int, []string, error) {
```

In the same function, change the `composeImage` invocation from:

```go
		if err := composeImage(ctx, img, bundle, rb, destRef,
			opts.SystemContext, opts.AllowConvert, opts.reporter()); err != nil {
			return 0, nil, err
		}
```

to:

```go
		if err := composeImage(ctx, img, bundle, rb, destRef,
			opts.SystemContext, opts.AllowConvert, opts.reporter(), cache); err != nil {
			return 0, nil, err
		}
```

- [ ] **Step 2: Construct the cache in `Import` and pass it down**

In `Import`, change the `importEachImage` call site (currently `pkg/importer/importer.go:104`) from:

```go
	imported, skipped, err := importEachImage(ctx, bundle, resolvedByName, outputs, opts)
```

to:

```go
	cache := newBaselineBlobCache()
	imported, skipped, err := importEachImage(ctx, bundle, resolvedByName, outputs, opts, cache)
```

- [ ] **Step 3: Verify compile**

```bash
go build ./...
```

Expected: clean build, no errors.

- [ ] **Step 4: Run all unit tests in pkg/importer**

```bash
go test ./pkg/importer/... -count=1
```

Expected: all PASS — the existing compose/import tests should be byte-identical in behavior, and the new blobcache unit tests still pass.

- [ ] **Step 5: Run with race detector**

```bash
go test ./pkg/importer/... -count=1 -race
```

Expected: PASS with no race warnings.

---

### Task 2.4: Run the multi-image integration test — expect GREEN

**Files:** none new.

- [ ] **Step 1: Run**

```bash
go test -tags integration ./cmd/... -run TestUnbundleCLI_BaselineBlobsFetchedExactlyOnce_MultiImage -count=1 -v
```

Expected:

```
--- PASS: TestUnbundleCLI_BaselineBlobsFetchedExactlyOnce_MultiImage
PASS
```

The `t.Logf` output should also show one entry per distinct baseline digest with `total cross-repo hits=1`.

- [ ] **Step 2: Run with race detector**

```bash
go test -tags integration ./cmd/... -run TestUnbundleCLI_BaselineBlobsFetchedExactlyOnce_MultiImage -count=1 -race
```

Expected: PASS, no race warnings.

If any race appears, it's almost certainly inside `s.baseline.GetBlob` (registry source upstream is concurrent-safe, but verify), or inside our cache (re-read `pkg/importer/blobcache.go` against the unit-test patterns in `pkg/exporter/fpcache.go`).

---

### Task 2.5: Add the single-image integration test (forward-protection)

**Files:**
- Modify: `cmd/apply_registry_integration_test.go`

- [ ] **Step 1: Append the new test to `cmd/apply_registry_integration_test.go`**

Add this function at the end of the file (after `TestApplyCLI_MissingManifestExit4`):

```go
// TestApplyCLI_BaselineBlobsFetchedExactlyOnce_SingleImage runs the
// standard v1→v2 fixture through the apply path against a docker://
// baseline and asserts every distinct baseline blob digest was
// fetched at most once. This is forward-protection: even if the
// current fixture happens not to expose duplicate baseline-blob
// fetches (it depends on which target layers reduce against which
// baseline layers), the assertion locks in the per-Import cache
// guarantee for any future fixture evolution that might reintroduce
// the regression.
func TestApplyCLI_BaselineBlobsFetchedExactlyOnce_SingleImage(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedOCIIntoRegistry(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	before := len(srv.BlobHits())

	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		registryDockerURL(t, srv, "app/v1"),
		"oci-archive:"+filepath.Join(tmp, "restored.tar"),
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "apply failed: %s", stderr)

	hits := make(map[digest.Digest]int)
	for _, h := range srv.BlobHits()[before:] {
		if h.Repo == "app/v1" {
			hits[h.Digest]++
		}
	}
	require.NotEmptyf(t, hits,
		"expected at least one baseline-blob fetch from app/v1 — fixture must exercise the baseline-fetch path")
	for d, n := range hits {
		t.Logf("baseline blob %s hits=%d", d, n)
		require.Equalf(t, 1, n,
			"baseline blob %s fetched %d times; want exactly 1 — dedup regression", d, n)
	}
}
```

- [ ] **Step 2: Add the `digest` import**

Add `"github.com/opencontainers/go-digest"` to the import block at the top of `cmd/apply_registry_integration_test.go` if it's not already present (the existing file doesn't import digest today, so this addition is required). The new import block becomes:

```go
import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/registrytest"
)
```

- [ ] **Step 3: Run the new test; expect PASS**

```bash
go test -tags integration ./cmd/... -run TestApplyCLI_BaselineBlobsFetchedExactlyOnce_SingleImage -count=1 -v
```

Expected:

```
--- PASS: TestApplyCLI_BaselineBlobsFetchedExactlyOnce_SingleImage
PASS
```

If the assertion fails because `len(hits) == 0` (no baseline-only or patch-from blobs in the v1/v2 fixture), the test cannot serve as forward-protection — change the fixture to one that does exercise baseline fetches, or accept this limitation by relaxing the `require.NotEmpty` to a `t.Logf` warning. Decide before committing.

---

### Task 2.6: Run the full integration suite

**Files:** none new.

- [ ] **Step 1: Run `cmd` integration tests**

```bash
go test -tags integration ./cmd/... -count=1
```

Expected: all PASS. Pay attention to:
- `TestApplyCLI_*` (existing apply integration tests) — must continue to pass byte-identically; the cache should not change semantics.
- `TestUnbundleCommand_BundleRoundTrip` — must still pass.

- [ ] **Step 2: Run `pkg/importer` integration tests**

```bash
go test -tags integration ./pkg/importer/... -count=1
```

Expected: all PASS, including the `TestImporter_LazyBaselineFetch_OnlyReferencedBlobsPulled` (orthogonal assertion on shipped-not-pulled).

- [ ] **Step 3: Run with race detector across both packages**

```bash
go test -tags integration -race ./cmd/... ./pkg/importer/... -count=1
```

Expected: PASS, no race warnings. Race detector validates that the new parallel layer-copy path (enabled by `HasThreadSafeGetBlob → s.baseline.HasThreadSafeGetBlob()`) is actually safe under concurrency.

---

### Task 2.7: Lint & gofmt

**Files:** none new.

- [ ] **Step 1: Format the touched files**

```bash
gofmt -w \
  pkg/importer/compose.go \
  pkg/importer/importer.go \
  cmd/apply_registry_integration_test.go \
  cmd/unbundle_registry_integration_test.go
```

Expected: no output, no diff.

- [ ] **Step 2: Vet**

```bash
go vet ./...
```

Expected: no output.

- [ ] **Step 3: Lint (if available)**

```bash
which golangci-lint && golangci-lint run ./pkg/importer/... ./cmd/...
```

Expected: no findings. If `unused` lints fire on the (intentionally-kept) `_` blank parameter in `composeImage`, it was already there; ignore.

---

### Task 2.8: Commit Phase 2

**Files:** stage the four touched files.

- [ ] **Step 1: Stage**

```bash
git add \
  pkg/importer/compose.go \
  pkg/importer/importer.go \
  cmd/apply_registry_integration_test.go \
  cmd/unbundle_registry_integration_test.go
git status -s
```

Expected:

```
M  pkg/importer/compose.go
M  pkg/importer/importer.go
M  cmd/apply_registry_integration_test.go
A  cmd/unbundle_registry_integration_test.go
```

- [ ] **Step 2: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(importer): dedupe baseline blob fetches across all images in a bundle

Per-Import() baselineBlobCache (added in the prior commit) is now
threaded through Import → importEachImage → composeImage →
bundleImageSource. fetchVerifiedBaselineBlob routes through
cache.GetOrLoad, so each distinct baseline blob digest is fetched at
most once per Import() call regardless of how many shipped patches
reference it or how many images in a multi-image bundle share it.

bundleImageSource.HasThreadSafeGetBlob now delegates to the
underlying baseline source instead of hard-coding false. For
docker:// baselines (the spec's primary registry-native case) this
unlocks parallel layer copy in copy.Image. For oci-archive: /
docker-archive: / openshift: baselines the underlying source still
returns false so behavior is unchanged.

Two CLI-level integration tests gate the change:
- TestApplyCLI_BaselineBlobsFetchedExactlyOnce_SingleImage —
  forward-protection on the standard v1→v2 fixture via diffah apply.
- TestUnbundleCLI_BaselineBlobsFetchedExactlyOnce_MultiImage —
  RED→GREEN regression gate via diffah unbundle: two pairs share
  baseline content; total cross-repo BlobHits per distinct digest
  must equal 1.

Closes Phase 2 Goal 4 on the import side. Refs:
docs/superpowers/specs/2026-04-25-importer-baseline-blob-dedup-design.md §4-§5
EOF
)"
git log -1 --format='%h %s'
```

Expected: a new commit with the subject above.

---

## Phase 3 — CHANGELOG

### Task 3.1: Add the CHANGELOG entry

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Read current top section**

```bash
sed -n '1,60p' CHANGELOG.md
```

The top section is `## [Unreleased] — Phase 4: Delta quality & throughput`. New entries land under it.

- [ ] **Step 2: Add a `### Bug fixes` subsection above `### Internal`**

Insert this block immediately before the existing `### Internal` line (use Edit rather than overwriting the whole file). The exact text to insert:

```markdown
### Bug fixes

- **Importer baseline-blob dedup (Phase 2 Goal 4 regression).** `diffah apply` and `diffah unbundle` now fetch each distinct baseline blob digest at most once per invocation, regardless of how many shipped patches reference it or how many images in a multi-image bundle share it. Previously, a baseline layer used both as `PatchFromDigest` and as a baseline-only layer was fetched twice; in multi-image bundles, blobs shared across pairs were fetched once per pair. Backed by a per-`Import()` `singleflight`-coordinated cache mirroring the export-side `pkg/exporter/fpcache.go`. `bundleImageSource.HasThreadSafeGetBlob` now delegates to the underlying baseline source, unlocking parallel layer copy via `copy.Image` for `docker://` baselines.

```

After insertion, `CHANGELOG.md` looks like:

```markdown
...
- Phase 3 archives apply byte-identically through Phase 4 importer
  (decoder cap was raised, never lowered).
- Sidecar schema unchanged.

### Bug fixes

- **Importer baseline-blob dedup (Phase 2 Goal 4 regression).** ...

### Internal

- New `pkg/exporter/workerpool.go` (errgroup-based bounded pool).
...
```

- [ ] **Step 3: Verify the file builds without markdown lint complaints**

```bash
which markdownlint && markdownlint CHANGELOG.md
```

If `markdownlint` not installed, skip. Otherwise expected: no findings.

- [ ] **Step 4: Re-read to confirm placement**

```bash
sed -n '1,80p' CHANGELOG.md
```

Confirm the new `### Bug fixes` subsection sits between `### Backward compat` and `### Internal`.

---

### Task 3.2: Commit Phase 3

**Files:** stage `CHANGELOG.md` only.

- [ ] **Step 1: Stage**

```bash
git add CHANGELOG.md
git status -s
```

Expected:

```
M  CHANGELOG.md
```

- [ ] **Step 2: Commit**

```bash
git commit -m "$(cat <<'EOF'
docs: CHANGELOG entry for importer baseline-blob dedup

Notes the Phase 2 Goal 4 regression closure, the per-Import dedup
cache, and the parallel-apply behavior change for docker:// baselines.
EOF
)"
git log --oneline master..HEAD
```

Expected: three commits ahead of master, with subjects in order:

```
<hash3> docs: CHANGELOG entry for importer baseline-blob dedup
<hash2> feat(importer): dedupe baseline blob fetches across all images in a bundle
<hash1> feat(importer): add baselineBlobCache type
<hash0> docs(importer): spec baseline blob dedup (Phase 2 G4 regression)
```

(`hash0` is the spec commit `d14ee03` that already exists on the branch.)

---

## Definition of done

- [ ] `pkg/importer/blobcache.go` and `blobcache_test.go` exist; all 5 unit tests pass under `-race`.
- [ ] `pkg/importer/compose.go` has `cache` field on `bundleImageSource`, delegates `HasThreadSafeGetBlob`, routes `fetchVerifiedBaselineBlob` through the cache.
- [ ] `pkg/importer/importer.go` constructs `cache := newBaselineBlobCache()` once per `Import` and threads it via `importEachImage` to `composeImage`.
- [ ] `cmd/apply_registry_integration_test.go::TestApplyCLI_BaselineBlobsFetchedExactlyOnce_SingleImage` passes.
- [ ] `cmd/unbundle_registry_integration_test.go::TestUnbundleCLI_BaselineBlobsFetchedExactlyOnce_MultiImage` passes (and was demonstrably RED before Task 2.2's wiring).
- [ ] `go test -race ./...` is clean.
- [ ] `go test -tags integration -race ./cmd/... ./pkg/importer/...` is clean.
- [ ] `go vet ./...` is clean.
- [ ] `CHANGELOG.md` `[Unreleased]` block has a `### Bug fixes` subsection describing the change.
- [ ] Branch `feat/importer-baseline-blob-dedup` has 4 commits ahead of master: spec + 3 implementation commits.
