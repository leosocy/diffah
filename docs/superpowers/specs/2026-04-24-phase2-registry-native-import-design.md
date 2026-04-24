# Phase 2 — Registry-Native Import — Design

- **Status:** Draft
- **Date:** 2026-04-24
- **Author:** @leosocy
- **Roadmap:** Phase 2 of
  `docs/superpowers/specs/2026-04-23-production-readiness-roadmap-design.md`
- **Builds on:** CLI redesign (PR #11) — which already reserved `docker://`,
  `oci:`, `dir:` transports on BASELINE-IMAGE; this phase makes them real.

## 1. Motivation

After the skopeo-inspired CLI redesign, `diffah` speaks a consistent
transport-prefixed grammar on the input side — but only `docker-archive:`
and `oci-archive:` actually work at runtime. Every other transport
(`docker://`, `oci:`, `dir:`) returns "reserved but not yet implemented."

This phase closes that gap on the **import path** — the `apply` and
`unbundle` verbs — so that:

- A baseline image can be read from a live registry (`docker://ghcr.io/org/app:v1`)
  without first `docker pull`-ing it to a tar.
- A reconstructed image can be pushed directly to a registry
  (`docker://ghcr.io/org/app:v2`) without `docker load` → `docker push`.
- Local OCI layout directories (`oci:/srv/cache/app-v1`) and plain image
  directories (`dir:/srv/cache/app-v1`) are accepted uniformly on both
  sides.

All registry traffic carries uniform authentication, TLS, and retry
controls modelled on skopeo's `SystemContext` flag block.

Export-side registry support (letting `diff` and `bundle` read baseline
and target images directly from a registry) is Phase 3.

## 2. Goals

1. `diffah apply` accepts `docker://`, `oci:`, `dir:`, `docker-archive:`,
   and `oci-archive:` on both BASELINE-IMAGE and TARGET-IMAGE.
2. `diffah unbundle` accepts the same transports via its new OUTPUT-SPEC
   JSON, which maps each image name to an arbitrary output reference.
3. A shared `--authfile`/`--creds`/`--tls-verify`/`--retry-times` flag
   block is installed on `apply` and `unbundle`, translated into a
   single `*types.SystemContext` that the service layer threads through
   every registry call. (`diff` and `bundle` pick up the same block in
   Phase 3 when they gain registry-source capability.)
4. **Lazy baseline layer fetch:** when a baseline is a `docker://`
   reference, only the blobs referenced by patch-encoded entries in the
   delta cross the wire. The baseline manifest is always fetched; no
   other layers are.
5. Registry failures are classified into the existing exit-code
   taxonomy: auth → user/2, network/DNS/timeout → environment/3,
   manifest/schema/blob-digest → content/4.
6. Progress bars report pull and push phases per-blob, reusing Phase 1
   infrastructure.

### Non-goals

- Registry-source support on `diff` / `bundle`. Flags are registered
  there in this phase so the surface is stable, but `docker://` etc. on
  BASELINE-IMAGE / TARGET-IMAGE for those verbs remains "reserved" until
  Phase 3.
- Signature and signature verification (Phase 3).
- Persistent on-disk blob cache across runs. This phase uses per-process
  temp scratch that is removed on exit.
- Mirror lists, pull-through caches, and registry high-availability
  configurations.
- The `docker-daemon:`, `containers-storage:`, `ostree:`, `sif:`, and
  `tarball:` transports. They stay "reserved."
- Changes to the sidecar schema or on-disk delta-archive format.

## 3. Command surface

### 3.1 `apply` — single-image reconstruction

```
diffah apply DELTA-IN BASELINE-IMAGE TARGET-IMAGE [flags]

Arguments:
  DELTA-IN         path to the delta archive produced by 'diffah diff'
  BASELINE-IMAGE   image to apply the delta on top of (transport:path)
  TARGET-IMAGE     where to write the reconstructed image (transport:path)

Examples:
  # Registry round-trip
  diffah apply delta.tar docker://ghcr.io/org/app:v1 docker://ghcr.io/org/app:v2

  # Registry baseline → OCI layout directory
  diffah apply delta.tar docker://ghcr.io/org/app:v1 oci:/srv/cache/app-v2

  # Local tar baseline → registry push
  diffah apply delta.tar docker-archive:/tmp/old.tar docker://harbor.example.com/app:v2
```

**Positional changes from the CLI redesign baseline:**

- **TARGET-OUT → TARGET-IMAGE** — the output positional now requires a
  transport prefix, mirroring BASELINE-IMAGE. The transport prefix alone
  determines the output format.

**Flag changes:**

- **Removed:** `--image-format` — transport prefix is authoritative.
  When the old flag is present on the command line the CLI emits a
  migration hint ("this flag was removed; select format via the
  TARGET-IMAGE transport prefix") classified as user/2.
- **Kept:** `--allow-convert` — still governs cross-format media-type
  conversion (e.g. Docker schema-2 source written to `oci:`).
- **Kept:** `--dry-run`/`-n` — behaviour unchanged; verifies baseline
  reachability without writing or pushing.
- **Added:** the 10-flag registry & transport block (§4).

### 3.2 `unbundle` — multi-image reconstruction

```
diffah unbundle DELTA-IN BASELINE-SPEC OUTPUT-SPEC [flags]

Arguments:
  DELTA-IN        path to the bundle archive produced by 'diffah bundle'
  BASELINE-SPEC   JSON spec mapping image name -> baseline image reference
  OUTPUT-SPEC     JSON spec mapping image name -> output image reference

Examples:
  # Registry input + registry output
  diffah unbundle bundle.tar baselines.json outputs.json

  # Mixed destinations: registry for one, local tar for another
  diffah unbundle --strict bundle.tar baselines.json outputs.json
```

**Positional changes:**

- **OUTPUT-DIR → OUTPUT-SPEC** — the output positional now points at a
  JSON spec symmetric with BASELINE-SPEC. Mixed destinations (one image
  pushed to a registry, another saved as a local tar) become a
  first-class feature of this grammar.
- BASELINE-SPEC's `baselines` map values may now be any transport
  reference; previously they were filesystem paths only.

**Flag changes:**

- **Removed:** `--image-format`.
- **Kept:** `--strict`, `--allow-convert`, `--dry-run`/`-n`.
- **Added:** the 10-flag registry & transport block.

### 3.3 `diff` / `bundle` — export side (unchanged in Phase 2)

Both verbs are untouched by this phase. Non-archive transports on
BASELINE-IMAGE (for `diff`) and inside BUNDLE-SPEC values (for
`bundle`) continue to return "reserved but not yet implemented." The
registry & transport flag block is **not** registered on these verbs in
Phase 2 — the flags would be dead weight as long as the positionals
can't resolve a registry reference. Phase 3 adds both capabilities
together.

### 3.4 Spec file schemas

**BASELINE-SPEC** — existing shape, widened value type:

```json
{
  "baselines": {
    "svc-a": "docker://ghcr.io/org/svc-a:v1",
    "svc-b": "oci-archive:/srv/cache/svc-b-v1.tar"
  }
}
```

**OUTPUT-SPEC** — new, symmetric with BASELINE-SPEC:

```json
{
  "outputs": {
    "svc-a": "docker://ghcr.io/org/svc-a:v2",
    "svc-b": "oci-archive:/srv/cache/svc-b-v2.tar"
  }
}
```

Both specs are parsed by `pkg/diff` helpers (`ParseBaselineSpec` and
new `ParseOutputSpec`), which each call
`alltransports.ParseImageName` on every value before returning. Syntax
errors surface as user/2 with the spec file path and the offending key
in the error message.

**Strict transport prefix in spec values** — every entry in BASELINE-SPEC
and OUTPUT-SPEC must carry a transport prefix, symmetric with
BASELINE-IMAGE / TARGET-IMAGE positionals. A bare filesystem path in a
spec value fails as user/2 with a "missing transport prefix" error
pinned to the offending key, and the same "Did you mean: `docker-archive:…`"
hint as the positional parser when the tail looks like a tar.

**Spec file existing consumers** — `diff.ParseBaselineSpec` currently
joins relative filesystem paths against the spec's directory. After
Phase 2 it no longer joins anything: the value is parsed as a
transport reference and passed through unchanged. This is a breaking
change for existing BASELINE-SPEC JSON files; they must be edited to
add transport prefixes (almost always `docker-archive:` or
`oci-archive:`).

**Key-set enforcement:**

- On `unbundle`, every image name in the sidecar must appear as a key
  in OUTPUT-SPEC. Missing keys are user/2 with the missing name listed.
- BASELINE-SPEC retains its current `--strict` semantics — missing
  baselines either skip (default) or error (`--strict`).

## 4. Registry & transport flag block

Installed on `apply`, `unbundle`, `diff`, `bundle` via a shared
`installRegistryFlags(cmd)` helper.

| Flag | Default | Semantics |
|---|---|---|
| `--authfile PATH` | precedence chain (below) | Docker-config-shaped JSON file containing per-registry credentials |
| `--creds USER[:PASS]` | unset | One-shot inline credentials (prompt for password if `:PASS` omitted and stdin is a TTY) |
| `--username string` | unset | Split-form username |
| `--password string` | unset | Split-form password (requires `--username`) |
| `--no-creds` | unset | Force anonymous access; mutually exclusive with all other credential flags |
| `--registry-token TOKEN` | unset | Bearer token; mutually exclusive with `--creds`/`--username`/`--password` |
| `--tls-verify` | `true` | Require HTTPS and verify the server certificate |
| `--cert-dir PATH` | unset | Directory of client certificates (`*.crt`, `*.cert`, `*.key`) for mTLS |
| `--retry-times N` | `3` | Retry count for transient failures (429, 5xx, connection reset) |
| `--retry-delay DURATION` | exponential backoff | Fixed delay between retries when set; otherwise exponential |

**Default authfile precedence** (when no credential flag is given):
1. `$REGISTRY_AUTH_FILE` if set and the file exists.
2. `$XDG_RUNTIME_DIR/containers/auth.json` if it exists.
3. `$HOME/.docker/config.json` if it exists.
4. Otherwise anonymous.

**Mutual-exclusion matrix** (violations are user/2):

| | `--creds` | `--username`/`--password` | `--no-creds` | `--registry-token` | `--authfile` |
|---|---|---|---|---|---|
| `--creds` | – | ✗ | ✗ | ✗ | OK (file is fallback) |
| `--username`/`--password` | ✗ | – | ✗ | ✗ | OK |
| `--no-creds` | ✗ | ✗ | – | ✗ | ✗ |
| `--registry-token` | ✗ | ✗ | ✗ | – | OK |
| `--authfile` | OK | OK | ✗ | OK | – |

**Non-retryable conditions:** 4xx (other than 429), TLS verification
failure, malformed manifest. These surface immediately with the
matching exit category.

## 5. Architecture

### 5.1 Package layout changes

```
internal/imageio/
  reference.go          # already exists: alltransports.ParseImageName wrapper
  sysctx.go   (new)     # BuildSystemContext(cliFlags) (*types.SystemContext, error)
  sniff.go              # OpenArchiveRef stays for DELTA-OUT / tests only

pkg/diff/
  bundle_spec.go        # + ParseOutputSpec + OutputSpec type
  errors.go             # + ErrRegistry{Auth,Network,ManifestMissing,ManifestInvalid}
  classify_registry.go (new)  # helper that wraps upstream registry errors

pkg/importer/
  importer.go           # Options: rename OutputPath/OutputFormat → Outputs map
                        #          + SystemContext field
  resolve.go            # resolveBaselines: lazy ImageSource instead of
                        #   eager full-image fetch
  compose.go            # composeImage: dest is types.ImageReference, not path+format
  lazyblob.go (new)     # wraps types.ImageSource so composeImage asks for blobs
                        #   by digest on demand

cmd/
  transport.go          # widen supportedInputTransports; alltransports-validate refs
  registry_flags.go (new)  # installRegistryFlags + build SystemContext closure
  apply.go              # drop scratch+rename; pass ImageRef to importer; register flags
  unbundle.go           # parse OUTPUT-SPEC; pass map to importer; register flags
  # diff.go, bundle.go — NOT touched in Phase 2; changes land in Phase 3
```

### 5.2 Data flow — `apply docker://baseline docker://target`

```
cmd.runApply
  ├── ParseImageRef("BASELINE-IMAGE", args[1])     → ImageRef{Transport, Path, Raw}
  ├── ParseImageRef("TARGET-IMAGE", args[2])       → ImageRef
  ├── installRegistryFlags.build()                 → *types.SystemContext
  └── importer.Import(ctx, Options{
        DeltaPath:        args[0],
        Baselines:        {"default": baseline.Raw},   // full "transport:path"
        Outputs:          {"default": target.Raw},
        SystemContext:    sc,
        Strict:           true,
        AllowConvert:     false,
        ProgressReporter: reporter,
      })

importer.Import
  ├── extractBundle(DeltaPath) → sidecar + per-blob temp files
  ├── resolveBaselines(sc, baselinesMap, sysctx, strict=true)
  │     for each name:
  │       ref, _ := imageio.ParseReference(raw, sysctx)
  │       src, _ := ref.NewImageSource(ctx, sysctx)
  │       manifest, _ := src.GetManifest(ctx, nil)
  │       verify digest.FromBytes(manifest) == sidecar.Images[name].Baseline.ManifestDigest
  │       return resolvedBaseline{Name, Ref, Src, Manifest}  // Src held open for lazy fetch
  │
  └── composeImage(sidecar.Images[name], bundle, resolved, outputsMap, sysctx)
        dest, _ := imageio.ParseReference(outputsMap[name], sysctx)
        for each blob in manifest:
          if blob in sidecar (full/patch):    pull from bundle
          else (baseline-only):               resolved.Src.GetBlob(ctx, BlobInfo{Digest})  // lazy
          if patch:                            zstd --patch-from + resolved.Src.GetBlob(...)
        copy.Image(ctx, policyCtx, dest, composedSrc, copyOpts)
```

### 5.3 Lazy baseline fetch details

The library's `resolvedBaseline` holds an open `types.ImageSource` for
each baseline (not a pre-fetched image). In `composeImage`:

- For a layer shipped in the delta as `encoding=full` or `encoding=patch`
  → the bytes come from the delta archive; the baseline source is not
  touched for that blob.
- For a layer present in the target manifest but **not** shipped in the
  delta → the bytes are the baseline's bytes verbatim; `src.GetBlob(ctx,
  BlobInfo{Digest}, cache)` fetches the blob from the baseline source.
  For `docker://` this is a single HTTP range request to
  `/v2/<repo>/blobs/<digest>`; for filesystem baselines it is a local
  read.
- The patch-apply path (`encoding=patch`) additionally asks the
  baseline source for the base blob referenced by the patch.

Per-run temp scratch: `os.MkdirTemp("diffah-pull-*")` holds any
materialised baseline bytes that zstd needs on-disk for `--patch-from`.
The directory is removed in a deferred cleanup in
`importer.Import`. No cross-run cache.

### 5.4 Progress reporting

`copy.Image` accepts a `copy.Options.Progress` chan receiving
`types.ProgressProperties`. The importer adapts this into the existing
`progress.Reporter`:

- Baseline pull: one bar per lazy-fetched blob, labelled
  `pulling baseline:<name> <digest>`.
- Target push: one bar per blob copy operation emitted by
  `copy.Image`, labelled `pushing target:<name> <digest>`.
- Phases announced on the reporter: `resolving-baselines`, `pulling`,
  `composing`, `pushing`, `done`.

### 5.5 Error classification

`classifyRegistryErr(err) error` inspects the error chain for
upstream go.podman.io error types and stdlib network types:

- `errors.As(&docker.ErrUnauthorizedForCredentials)` → `&ErrRegistryAuth`.
- `errors.As(&url.Error)` with an embedded `net.OpError` whose Op is
  `dial` / `read` / `write` → `&ErrRegistryNetwork`.
- HTTP 404 on a manifest request → `&ErrRegistryManifestMissing`.
- Manifest JSON decode failure or unexpected media type → `&ErrRegistryManifestInvalid`.
- Anything not recognised → pass through unchanged so the existing
  `errs.Classify` fallbacks (filesystem, context, etc.) still apply.

All four new error types implement `errs.Categorized` + `errs.Advised`
with hints matching §2.

## 6. Testing

### 6.1 In-process registry harness (`internal/registrytest`)

Wraps `github.com/google/go-containerregistry/pkg/registry` with:

| Option | Purpose |
|---|---|
| `WithBasicAuth(user, pass)` | HTTP Basic middleware |
| `WithBearerToken(token)` | Bearer-only middleware |
| `WithTLS(cert, key)` | HTTPS serving with the supplied materials |
| `WithInjectFault(matcher, responder)` | Inject 5xx for retry testing |
| `.Hits() []BlobRequest` | Access log for lazy-fetch assertions |
| `.Seed(ref, image)` | Preload an image into the registry |

Self-signed cert + key are generated per-test with `crypto/tls`. The
harness exposes only `httptest.Server` plus helpers — no dependency on
Docker, Podman, or any running registry.

### 6.2 Unit tests

| File | Coverage |
|---|---|
| `cmd/transport_test.go` (extend) | `docker://`, `oci:`, `dir:` accepted; `docker-daemon:`/`containers-storage:`/`ostree:`/`sif:`/`tarball:` still reserved; syntactically invalid refs (`docker://`, `oci:`) rejected as user/2 |
| `cmd/registry_flags_test.go` (new) | Each flag maps to the correct `SystemContext` field; mutual-exclusion matrix enforced; authfile precedence chain tested with a fake HOME + XDG_RUNTIME_DIR |
| `cmd/apply_test.go` (extend) | Drop `--image-format`; TARGET-IMAGE with bare path rejected; OUTPUT-SPEC-shaped error surfaced |
| `cmd/unbundle_test.go` (extend) | OUTPUT-SPEC parses; missing image-name in spec rejected; every sidecar name must have an output entry |
| `pkg/diff/output_spec_test.go` (new) | Valid/invalid shapes; transport validation on each value |
| `pkg/diff/errors_test.go` (extend) | `ErrRegistry*` classification |
| `pkg/importer/resolve_test.go` (extend) | `resolveBaselines` returns an open source (not a byte buffer) |
| `pkg/importer/lazyblob_test.go` (new) | `src.GetBlob` invoked exactly for referenced-only layers |

### 6.3 Integration tests (`//go:build integration`)

All tests drive a fresh in-process registry per `t.Run`. Listed in
Section 3 of the brainstorm message; recorded here as the acceptance
bar:

1. Anonymous pull baseline → restored image matches expected digest.
2. Basic-auth pull (`--creds`, `--username`/`--password`, `--authfile`).
3. Bearer-token pull (`--registry-token`).
4. TLS default-verify against self-signed registry fails until
   `--cert-dir` supplied; `--tls-verify=false` bypasses.
5. Push to fresh tag — pushed manifest readable via a second client.
6. Lazy-fetch assertion — only the referenced-blob digest appears in
   `registrytest.Hits()`.
7. Retry on 503 with `--retry-times 3` succeeds; with `--retry-times 0`
   fails immediately.
8. 401 → exit 2, connection refused → exit 3, 404 manifest → exit 4.
9. `unbundle` with mixed-destination OUTPUT-SPEC writes all targets
   correctly.

### 6.4 Manual acceptance

Against a real public registry (`ghcr.io/leosocy/diffah-test` or user's
own):

```bash
# Export side unchanged (Phase 3 extends this to docker://).
diffah diff docker-archive:v1.tar docker-archive:v2.tar delta.tar

# New in Phase 2:
diffah apply delta.tar \
  docker://ghcr.io/leosocy/diffah-test:v1 \
  docker://ghcr.io/leosocy/diffah-test:v2

# Verify tag readable by another client:
skopeo inspect docker://ghcr.io/leosocy/diffah-test:v2
```

## 7. Migration notes

### 7.1 Breaking changes (bundled with Phase 2)

1. `apply`'s TARGET-OUT positional → TARGET-IMAGE. A bare filesystem
   path (no transport prefix) now fails with the same "missing
   transport prefix" error as BASELINE-IMAGE, with a "Did you mean"
   hint for paths that end in `.tar` / `.tgz`.
2. `unbundle`'s OUTPUT-DIR positional → OUTPUT-SPEC. A directory path
   is caught by the spec parser ("not a valid JSON file") with a hint
   pointing at the OUTPUT-SPEC schema.
3. `--image-format` removed from `apply` and `unbundle`. The flag
   appearing on a command line produces a dedicated error (user/2)
   with the migration hint "select format via the transport prefix of
   TARGET-IMAGE / OUTPUT-SPEC entries."

### 7.2 Non-breaking

- All existing `docker-archive:` and `oci-archive:` invocations that
  already use transport prefixes (which is all of them after the CLI
  redesign) continue to work. Their data flow now goes through
  `alltransports.ParseImageName` instead of the file-path sniff, but
  the user-visible behaviour is identical.
- `inspect` and `doctor` are untouched.

### 7.3 CHANGELOG

A new `## [Unreleased] — Phase 2: Registry-native import` block
enumerates the above plus the added transports, flags, and spec
formats.

## 8. Rollout

Each stage lands as its own commit (or small commit series) on a single
feature branch. The CLI wiring is itself the Phase 2 feature gate — no
env-var flag is needed, because a user on the current main branch never
reaches the new code paths until the CLI renames and flag-block
registrations ship.

1. Library surface: add `ParseOutputSpec`, `ErrRegistry*`, widen
   `importer.Options` + `resolveBaselines` to hold an open
   `types.ImageSource` per baseline. All behind the existing
   fallback sniff — zero behavioural change.
2. Lazy blob fetch + `copy.Image` wiring in `composeImage`. Still no
   CLI surface change — filesystem baselines keep working via the
   new path with identical bytes.
3. In-process registry harness under `internal/registrytest`, with its
   own unit tests.
4. Integration tests for library-layer registry pull and push, using
   the harness.
5. CLI: widen `supportedInputTransports`; add `registry_flags.go`;
   rewire `apply` and `unbundle` (positional renames,
   `--image-format` removal, flag-block installation).
6. CLI integration tests under `cmd/apply_registry_integration_test.go`
   and `cmd/unbundle_registry_integration_test.go`.
7. Breaking-change hints in the removed-verb trap + migration section
   of the CHANGELOG.
8. Manual acceptance against a real registry, then ship.

## 9. Open questions

None blocking. The following are deliberate choices the design makes
and can be revisited post-Phase 2 if user feedback warrants:

- Persistent blob cache (`~/.cache/diffah/blobs/<digest>`) — deferred.
- Parallel blob fetch — deferred; the upstream `copy.Image` already
  parallelises per-blob copies, so this would only affect the
  lazy-baseline side.
- Platform selection for manifest-list baselines — `--platform` already
  exists on `diff`/`bundle`; for `apply`/`unbundle` the sidecar carries
  the target platform, so no new flag.
