# Phase 3 — Registry-Native Export + Signing — Design

- **Status:** Draft
- **Date:** 2026-04-24
- **Author:** @leosocy
- **Roadmap:** Phase 3 of
  `docs/superpowers/specs/2026-04-23-production-readiness-roadmap-design.md`
- **Builds on:**
  - Phase 2 import-side registry surface (PR #14) — reuses the 10-flag
    registry block, `SystemContext` plumbing, `registrytest` harness, and
    error-category taxonomy.
  - CLI redesign (PR #11) — transport-prefixed positional grammar,
    removed-command migration hints, structured error printing.

## 1. Motivation

After Phase 2, diffah's registry surface is asymmetric: the **import**
verbs (`apply`, `unbundle`) read from and write to any
`go.podman.io/image/v5` transport, while the **export** verbs (`diff`,
`bundle`) still require local archive sources. Phase 3 closes that gap —
`diff` and `bundle` gain the same transport-prefixed grammar, the same
`--authfile`/`--creds`/`--tls-verify`/... block, and the same
`types.SystemContext` threading through the exporter.

In parallel, Phase 3 adds a trust layer. Users can sign a delta archive
with a cosign-compatible keyed signature, and consumers can verify it.
The subject of the signature is the canonicalized sidecar JSON digest
(`sha256(jcs(sidecar.json))`), not the whole tar — layer blobs are
already content-addressed transitively through the sidecar, so a valid
signature over the sidecar is equivalent to signing the delta's full
content and much cheaper to compute. Three cosign-standard sidecar
files — `OUT.sig`, `OUT.cert`, `OUT.rekor.json` — live next to the
archive on disk (no tar embedding in v1).

## 2. Goals (exit criteria)

A Phase 3 PR train is mergeable when **all eight** hold:

1. **`diff` accepts registry sources.** `diffah diff
   docker://host/repo:v1 docker://host/repo:v2 delta.tar` round-trips
   against the in-process `registrytest` harness. Anonymous pull,
   basic-auth pull, bearer-token pull, and `--tls-verify=false` /
   `--cert-dir` mTLS pull each have an integration test.
2. **`bundle` accepts registry sources via BundleSpec.** Every
   `baseline`/`target` value in BundleSpec must carry a transport
   prefix; bare paths fail with a Phase-2-shaped error and a
   copy-pasteable `sed` migration hint.
3. **`SystemContext` threaded end-to-end on export.** `exporter.Options`
   gains a `SystemContext *types.SystemContext` field plus `RetryTimes
   int` / `RetryDelay time.Duration`. Every `ImageReference.NewImageSource`
   / `GetManifest` / `GetBlob` call in `pkg/exporter` receives `sys`.
   No code path silently uses `nil` when the user supplied credentials.
4. **Export-side content-similarity bandwidth is bounded.**
   Fingerprinting streams tar entries through `io.Reader` adapters
   without materializing decompressed layers in memory; peak RSS stays
   within `O(workers × layer-chunk)`, not `O(sum-of-baseline-layer-sizes)`.
   `docs/performance.md` documents the bandwidth cost explicitly
   ("baseline layers must be read; they are not retained").
5. **`diff`/`bundle --sign-key PATH`** writes `OUT.sig` as a
   base64-encoded DER ECDSA-P256 signature over
   `sha256(jcs(sidecar.json))`. `.cert` is not written in keyed mode.
   `.rekor.json` is written only when `--rekor-url URL` is supplied.
   External `cosign verify-blob` succeeds on our `.sig` output, asserted
   by a CI snapshot test.
6. **`apply`/`unbundle --verify PATH`** succeeds on a valid sig, exits 4
   on a bad key match, and exits 4 when the archive is unsigned. Absent
   `--verify` is byte-identical to today's behavior on both signed and
   unsigned archives.
7. **Registry failures classify cleanly.** Auth 401/403 → user/2 (with
   `--authfile` hint), network/DNS/timeout → env/3, manifest/schema/
   blob-digest mismatch → content/4 — identical taxonomy to Phase 2's
   import-side.
8. **Progress bars cover every new phase.** Pull and push per-blob on
   registry references; a new `signing` phase line on sign; a
   `verifying` phase line on verify. TTY/non-TTY fallback unchanged
   from Phase 1.

### Non-goals (explicit anti-goals)

- **No cross-phase scope creep.** Keyless signing, KMS signing
  (`cosign://...` URIs), `--sign-cert` (attaching pre-existing x509),
  `--sign-inline` (embedding sigs in the tar), Rekor search, multi-sig
  (co-signing) — all deferred. If a user opens an issue asking, we
  point at this section and track as a follow-on.
- **No sidecar schema version bump.** The `.sig`/`.cert`/`.rekor.json`
  are external artifacts; the sidecar JSON format inside `diffah.json`
  is unchanged. Schema version policy (from Phase 1) therefore does
  not trigger.
- **No `--sign-if-key-available` convenience.** Signing is always an
  explicit opt-in via `--sign-key`. Missing key means unsigned archive.
- **No changes to the delta-archive tar layout or the intra-layer
  backend.** Phase 3 is a CLI/exporter-wrapper phase plus a signer
  module; the encoding pipeline is untouched.
- **No implicit trust-store lookup on verify.** `--verify` accepts only
  an explicit key path. We do not scan
  `~/.config/diffah/trusted-keys/`, sigstore public-good roots, or OS
  keyrings.
- **No persistent cross-run blob cache on export.** Per-process temp
  scratch removed on exit, same as Phase 2.
- **No mirror lists or pull-through caches.**
- **No `docker-daemon:` / `containers-storage:` / `ostree:` /
  `sif:` / `tarball:` transports.** They stay "reserved."

## 3. Command surface

### 3.1 `diff` — export-side registry support + signing

```
diffah diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT [flags]

Arguments:
  BASELINE-IMAGE   older image to diff against (transport:path)
  TARGET-IMAGE     newer image whose contents become the diff target (transport:path)
  DELTA-OUT        filesystem path to write the delta archive

Registry & transport:
      --authfile PATH            authentication file
      --creds USER[:PASS]        inline credentials
      --username string          registry username
      --password string          registry password
      --no-creds                 access the registry anonymously
      --registry-token TOKEN     bearer token for registry access
      --tls-verify               require HTTPS + verify certificates (default true)
      --cert-dir PATH            client certificates directory
      --retry-times N            retry count (default 3)
      --retry-delay DURATION     fixed inter-retry delay (default: exponential)

Signing:
      --sign-key PATH            ECDSA-P256 private key (PEM or cosign-boxed PEM)
      --sign-key-password-stdin  read key passphrase from stdin
      --rekor-url URL            upload signature to this Rekor instance (default: none)
```

New examples:

```bash
# Registry → registry diff
diffah diff docker://ghcr.io/org/app:v1 docker://ghcr.io/org/app:v2 delta.tar

# Signed delta
diffah diff \
  --sign-key cosign.key \
  --sign-key-password-stdin \
  docker://ghcr.io/org/app:v1 docker://ghcr.io/org/app:v2 \
  delta.tar < passphrase.txt
# Writes: delta.tar, delta.tar.sig
```

### 3.2 `bundle` — multi-image + signing

Same registry & signing flag block as `diff`. BundleSpec grammar
change (breaking):

```json
{
  "pairs": [
    {"name": "svc-a",
     "baseline": "docker://ghcr.io/org/svc-a:v1",
     "target":   "docker://ghcr.io/org/svc-a:v2"},
    {"name": "svc-b",
     "baseline": "docker-archive:./v1/svc-b.tar",
     "target":   "oci:/srv/builds/svc-b-v2"}
  ]
}
```

Migration error (on an existing bare-path bundle.json):

```
diffah: user: invalid BUNDLE-SPEC "bundle.json":
  pairs[0].baseline: missing transport prefix ("v1/svc-a.tar")
  hint: prefix archive paths with 'docker-archive:' —
        sed -E -i '' 's|(\"baseline\"\|\"target\"): \"([^:\"]*\.tar[a-z]*)\"|\1: \"docker-archive:\2\"|g' bundle.json
```

### 3.3 `apply` / `unbundle` — verification

Only the signing half of Phase 3 touches these verbs (registry support
landed in Phase 2). New flags:

```
Verification:
      --verify PATH              ECDSA-P256 public key (PEM) — require signature match
      --verify-rekor-url URL     fetch Rekor inclusion proof from here (default: none)
```

Behavior matrix:

| Archive | `--verify` | Outcome |
|---|---|---|
| signed | supplied + key matches | exit 0 |
| signed | supplied + key mismatch | exit 4 (content) — "signature does not verify under the supplied public key" |
| signed | absent | exit 0 — signature silently ignored (backward-compat) |
| unsigned | supplied | exit 4 (content) — "archive has no signature; `--verify` requires a signed archive" |
| unsigned | absent | exit 0 |

Examples:

```bash
# Registry round-trip with signature check
diffah apply --verify cosign.pub delta.tar docker://reg/app:v1 docker://reg/app:v2

# Multi-image with per-image output spec + verification
diffah unbundle --verify cosign.pub bundle.tar baselines.json outputs.json
```

### 3.4 Reserved-for-later syntax

Two "hold-open-for-later" reserved shapes:

- `diff --sign-key cosign://some-kms-uri ...` → `user/2 error:
  "cosign:// KMS URIs are reserved but not yet implemented (Phase 3
  supports file-path keys only)"`.
- `diff --keyless ...` → same class — `--keyless` flag is registered
  hidden (`MarkHidden("keyless")`) and rejects with an
  implementation-deferred hint pointing at the roadmap.

## 4. Code-level shape

### 4.1 New package: `pkg/signer`

```
pkg/signer/
├── signer.go      // Sign(ctx, SignRequest) (*Signature, error)
├── verifier.go    // Verify(ctx, pubKeyPath, payload, *Signature, rekorURL) error
├── cosign.go      // cosign-format emitter: writes .sig/.cert/.rekor.json sidecars
├── canonical.go   // JCSCanonical(v any) ([]byte, error) — RFC 8785 JSON Canonicalization
├── rekor.go       // optional: UploadEntry(ctx, rekorURL, sig) (*RekorBundle, error)
└── signer_test.go // keypair fixtures + cosign verify-blob snapshot test
```

Public API (kept narrow):

```go
type Signature struct {
    Raw         []byte // DER ECDSA
    CertPEM     []byte // empty in keyed mode
    RekorBundle []byte // populated only when --rekor-url is set
}

type SignRequest struct {
    KeyPath         string
    PassphraseBytes []byte // from stdin; the signer zeros this slice in-place after key decryption completes (caller must not read it post-Sign)
    RekorURL        string // empty → no upload
    Payload         []byte // sha256(jcs(sidecar.json))
}

func Sign(ctx context.Context, req SignRequest) (*Signature, error)
func Verify(ctx context.Context, pubKeyPath string, payload []byte, sig *Signature, rekorURL string) error
func WriteSidecars(archivePath string, sig *Signature) error // emits .sig / .cert / .rekor.json
func LoadSidecars(archivePath string) (*Signature, error)     // nil,nil if none present
```

Why a separate package: keeps sigstore imports out of `pkg/exporter`
and `pkg/importer` (which already carry containers-image, zstd, and
archive deps). Test-only fixtures (ECDSA-P256 keypairs) live under
`pkg/signer/testdata/` and are generated once, committed, and
documented as throwaway test material.

### 4.2 `pkg/diff` changes

- `ParseBundleSpec` — the `baseline`/`target` values now go through
  `validateTransportRef` (the existing helper that Phase 2 added for
  `ParseBaselineSpec` / `ParseOutputSpec`). `resolveSpecPath` is
  replaced by a transport-aware resolver: for `docker-archive:` /
  `oci-archive:` / `oci:` / `dir:` values, relative paths are resolved
  against the spec file directory (preserves today's "paths are
  relative to bundle.json" ergonomics); for `docker://`, the ref is
  parsed as-is.
- New error type: `ErrBundleSpecMissingTransport{FieldPath string}`
  (e.g. `FieldPath: "pairs[0].baseline"`) — the CLI layer catches it
  and emits the sed-migration hint.

### 4.3 `pkg/exporter` changes

- `exporter.Pair` — `BaselinePath` / `TargetPath` keep their string
  types but are renamed to `BaselineRef` / `TargetRef` (consistent
  with importer's `Baselines map[string]string`).
- `exporter.Options` adds:
  ```go
  SystemContext     *types.SystemContext
  RetryTimes        int
  RetryDelay        time.Duration
  SignKeyPath       string       // empty → no signing
  SignKeyPassphrase []byte       // zeroed after signer returns
  RekorURL          string
  ```
- `planPair` — `imageio.OpenArchiveRef(p.BaselinePath)` →
  `alltransports.ParseImageName(p.BaselineRef)`. Same change for
  `TargetRef`.
- `readManifestBundle` / `readBlobBytes` / `readBlob` — pass
  `o.SystemContext` through every `NewImageSource` call (they already
  take a `types.ImageReference` so this is a thread-only change).
- Content-similarity fingerprinter — today's `fingerprint()` reads
  whole-layer bytes into memory before tar-parsing. Refactor:
  `io.TeeReader` over the blob stream, `tar.Reader` consumes in-place,
  byte buffer flushed chunk-by-chunk. Bandwidth cost unchanged (must
  read every baseline layer), memory cost bounded.
- New phase hook: after `buildBundle` writes the archive and sidecar
  JSON, if `o.SignKeyPath != ""` the exporter: (a) reads the
  `diffah.json` tar entry back from the just-written archive, (b)
  canonicalizes those bytes via `signer.JCSCanonical`, (c) computes
  `sha256`, (d) calls `signer.Sign`, (e) calls
  `signer.WriteSidecars(archivePath, sig)`. `WriteSidecars` emits
  `.sig` unconditionally, `.cert` only when `sig.CertPEM != nil`
  (keyless — out of scope here), and `.rekor.json` only when
  `sig.RekorBundle != nil` (i.e. `--rekor-url` was set).

### 4.4 `pkg/importer` changes (small — verify only)

- `importer.Options` adds:
  ```go
  VerifyPubKeyPath string // empty → skip verification
  VerifyRekorURL   string
  ```
- At `Import` entry, after extracting the sidecar JSON and before
  touching blobs, if `VerifyPubKeyPath != ""`:
  1. `sig, err := signer.LoadSidecars(o.DeltaPath)` — returns
     `(nil, nil)` if no `.sig` neighbor exists.
  2. If `sig == nil` → `errs.ContentError{Msg: "archive has no
     signature", Hint: "omit --verify, or sign the archive with
     'diffah diff --sign-key'"}` → exit 4.
  3. Otherwise compute `payload := sha256(jcs(sidecar.json))` from the
     just-extracted sidecar bytes.
  4. `signer.Verify(ctx, o.VerifyPubKeyPath, payload, sig,
     o.VerifyRekorURL)`; failure → `errs.ContentError{Msg: "signature
     does not verify", Hint: "check the public key matches the
     signer"}` → exit 4.
- If `VerifyPubKeyPath == ""`, no `LoadSidecars` call. Signed archives
  are byte-identically processed as today.

### 4.5 `cmd` changes

- `cmd/diff.go` / `cmd/bundle.go` — install `installRegistryFlags(c)`
  (existing helper from Phase 2) + a new `installSigningFlags(c)`
  helper. `runDiff` / `runBundle` call the `buildSystemContext`
  closure and a new `buildSignRequest` closure.
- `cmd/apply.go` / `cmd/unbundle.go` — add `installVerifyFlags(c)`
  (new helper, two flags). `runApply` / `runUnbundle` populate
  `importer.Options.VerifyPubKeyPath` / `VerifyRekorURL`.
- `cmd/registry_flags.go` — no changes; helper is reused across all
  four verbs.
- `cmd/sign_flags.go` / `cmd/verify_flags.go` — new files, parallel
  structure to `registry_flags.go`.

### 4.6 What stays untouched

- `internal/zstdpatch`, `internal/oci`, `internal/archive`,
  `internal/imageio` (except `OpenArchiveRef` loses its two callers
  in `planPair` — it remains used by the importer's baseline-archive
  fallback).
- Progress / slog / doctor / exit-code tables.
- The sidecar JSON schema.
- The on-disk tar layout of the delta archive.

## 5. Signing protocol (wire-format details)

### 5.1 The payload — what exactly gets signed

**Subject:** the SHA-256 of the RFC 8785 JCS canonicalization of the
sidecar JSON, computed from the exact bytes that land inside the delta
archive.

```
payload = sha256( jcs( parse(sidecar.json bytes inside archive) ) )
```

Concretely, the exporter — after writing the archive:

1. Re-reads the `diffah.json` tar entry from the just-written archive
   (not the in-memory struct — the on-disk bytes, so what is signed is
   what is shipped).
2. Parses as `map[string]any` through `encoding/json` (no
   sidecar-struct type — this insulates us from field-order surprises).
3. Emits canonical bytes via `jcs.Canonical` (object keys sorted
   lexicographically, numbers normalized per JCS §4, strings re-encoded
   per JCS §3.2.2).
4. `sha256` those bytes → 32-byte payload → pass to `signer.Sign`.

On the verifier side, the identical pipeline runs against the
`diffah.json` bytes extracted from the incoming archive. If one byte
changed in the sidecar (added field, reordered key, whitespace), the
payload changes and verification fails. Since the sidecar encodes
**every shipped blob's SHA-256 digest** and **every baseline ref +
required-digest tuple**, the sidecar digest is a transitive seal over
the whole delta.

Rationale for *not* hashing the archive tar:

- tar byte layout is sensitive to archive-tool choices and mtimes that
  are not semantically meaningful;
- recomputing the whole archive's digest on verify is GB-scale I/O for
  multi-image bundles;
- sidecar digest is sufficient because blobs inside the tar are
  content-addressed by the digests recorded *in* the sidecar.

### 5.2 The signature algorithm

- **Curve:** ECDSA P-256 (`crypto/ecdsa` with `elliptic.P256()`).
- **Key encoding on disk:** PEM (`-----BEGIN EC PRIVATE KEY-----` or
  cosign-boxed `-----BEGIN ENCRYPTED COSIGN PRIVATE KEY-----`).
  - Unencrypted keys: `x509.ParseECPrivateKey`.
  - Cosign-boxed keys: scrypt-derived key + `golang.org/x/crypto/nacl/secretbox`
    decryption (matches cosign 2.x format). The cmd layer reads the
    passphrase via `--sign-key-password-stdin` (single line, `\n`
    trimmed) and hands the byte slice to `signer.Sign` via
    `SignRequest.PassphraseBytes`. The signer zeros the slice in-place
    after the key is decrypted.
- **Signature encoding:** ASN.1 DER `(r, s)` (stdlib `ecdsa.SignASN1`).
- **Hashing:** payload is already 32 bytes, passed directly to
  `ecdsa.SignASN1` (no re-hashing).

### 5.3 Sidecar file layout

For an archive `OUT/delta.tar`:

- `OUT/delta.tar.sig` — file: base64-encoded DER signature, trailing
  `\n`, mode `0644`. Byte-identical to `cosign sign-blob delta.tar >
  delta.tar.sig`.
- `OUT/delta.tar.cert` — **not written** in keyed mode. (In a future
  keyless phase this holds the PEM-encoded Fulcio cert.) The absence
  of this file is the signal that the archive is keyed-signed.
- `OUT/delta.tar.rekor.json` — **written only if `--rekor-url URL` is
  supplied**. Content is the cosign 2.x bundle format: a JSON object
  with `{"base64Signature": ..., "Payload": {"body": ...,
  "integratedTime": ..., "logIndex": ..., "logID": ...}}`. If the
  archive was signed but never uploaded to Rekor, this file is absent.

### 5.4 The verify pipeline

Input: `delta.tar` path, public key PEM path, optional Rekor URL.

1. `sig := LoadSidecars(delta.tar)` — stats the three neighbor files.
   Returns `(nil, nil)` if **no `.sig`** is present. If `.sig` is
   present, reads it, base64-decodes; opens `.rekor.json` if present
   and deserializes; `.cert` is loaded only if present (keyed mode →
   nil).
2. Extract `diffah.json` bytes from the delta tar. Pass through the
   identical `jcs` + `sha256` pipeline as §5.1.
3. Parse the user-supplied public key PEM → `*ecdsa.PublicKey`.
4. `ecdsa.VerifyASN1(pub, payload, sig.Raw)` → `bool`. `false` →
   `errs.ContentError{Msg: "signature does not verify", ...}`.
5. If `sig.RekorBundle != nil` and user passed `--verify-rekor-url
   URL`, verify the Rekor inclusion proof against that URL's public
   key (fetched once, cached in-memory per process). Mismatch →
   `errs.ContentError{Msg: "Rekor inclusion proof does not verify",
   ...}`.
6. If `sig.RekorBundle == nil` but user passed `--verify-rekor-url URL`
   → warn only, not a failure. (Signed-without-Rekor is a legitimate
   offline case; `--verify-rekor-url` being set means "check it if
   present", not "require it".)

### 5.5 Compat-check with stock cosign

A CI gate in the new `pkg/signer` package:

1. Go test generates an ECDSA keypair, signs a fixed payload with our
   `Signer`.
2. Writes `OUT.sig` via `WriteSidecars`.
3. `exec.Command("cosign", "verify-blob", "--key", "pub.pem",
   "--signature", "OUT.sig", "PAYLOAD-FILE")` must exit 0.
4. Same dance in reverse: `cosign sign-blob --key priv.pem
   PAYLOAD-FILE > OUT.sig`, then our `Verify` must accept it.
5. The test is gated behind `DIFFAH_SIGN_COMPAT=1` + a `cosign` binary
   probe (skip if missing). Runs in the CI job that already installs
   sigstore tooling for release-signing; does not run on developer
   laptops by default.

This is the only place `cosign` binary interop matters — and it is
test-only, never in production runtime.

## 6. Backward compatibility

### 6.1 What breaks

- **BundleSpec JSON — every `baseline`/`target` value now requires a
  transport prefix.** Bare-path bundle.json files produced before
  Phase 3 error out on first parse with the sed-migration hint from
  §3.2. No silent compat shim. This is the sole user-facing breaking
  change in Phase 3.
- **`pkg/exporter.Pair` public field rename** — `BaselinePath` →
  `BaselineRef`, `TargetPath` → `TargetRef`. Downstream Go consumers
  (none known outside this repo) must update struct initializers.
  Mentioned in CHANGELOG under "internal" since the surface is thin.

### 6.2 What stays byte-identical

- **Unsigned archive workflows.** `diffah diff docker-archive:old.tar
  docker-archive:new.tar delta.tar` with no signing flags produces a
  `delta.tar` that differs from today's output only in the
  `sidecar.tool_version` field (the normal version bump).
- **`apply` / `unbundle` with no `--verify`** — ignores any
  `.sig`/`.cert`/`.rekor.json` sidecars next to the archive. A
  Phase-3-signed archive imports cleanly on a Phase-2 client (which
  has never heard of signatures) and on a Phase-3 client without
  `--verify`.
- **Phase 2 `BaselineSpec` / `OutputSpec` grammar** — unchanged. The
  transport-prefix requirement Phase 2 introduced stays exactly as-is;
  Phase 3 only brings `BundleSpec` into the same grammar.
- **Exit-code taxonomy** — 0/1/2/3/4 table unchanged. Signing/verify
  failures map into the existing `content` category (exit 4).
- **Sidecar JSON schema version** — stays at `v1`. Signing is an
  external artifact; the inner sidecar is untouched, so schema
  evolution policy does not trigger.
- **Progress / slog / doctor infrastructure** — unchanged. We add new
  phase strings (`"signing"`, `"verifying"`) and new doctor probes (key
  readability, sigstore trust-root presence if `--rekor-url` is set),
  but the contracts are unchanged.
- **Phase 2 registry flag block** — same 10 flags, same precedence
  chain, same error classification. Phase 3 just registers the block
  on two more verbs.

### 6.3 Forward compatibility

Reserved-for-later syntax stays reserved:

- `--sign-key cosign://kms-uri` — the `cosign://` scheme validator
  already fails with a reserved-but-unimplemented error. A future KMS
  phase fills in the implementation; the CLI shape does not change.
- `--keyless` flag — registered hidden (`MarkHidden("keyless")`), fails
  with the same reserved-but-unimplemented error. Unhides when a
  follow-on phase ships keyless.
- `--sign-inline` flag — not registered in Phase 3. If needed later,
  adding it is additive (new flag, default off).
- `.cert` sidecar file — never written in Phase 3 keyed mode, so Phase
  3 consumers that see a `.cert` on an archive simply load it without
  meaning. When keyless ships, producers start emitting `.cert` and
  Phase 3 consumers with `--verify` run keyed-verify against it and
  correctly fail (keyed public key will not match the Fulcio-issued
  private key), giving users a clear signal that they need to upgrade
  for keyless verification.

### 6.4 Migration docs

`CHANGELOG.md` gets a new `## [Unreleased] — Phase 3` section with:

1. The one breaking change (BundleSpec prefix requirement) as the
   first bullet with the sed migration snippet.
2. The `Pair` field rename as an internal note.
3. All additions (registry support on `diff`/`bundle`, signing/
   verification flags, `.sig`/`.cert`/`.rekor.json` sidecars).
4. The non-goals (deferred items) spelled out.

`docs/compat.md` gets a new "Signatures" section documenting: payload
construction (jcs → sha256), sidecar file naming, verify matrix,
forward-compat reservation of keyless slots.

## 7. Testing strategy

### 7.1 Unit — `pkg/signer`

- **`jcs.Canonical` property tests** — 20 random JSON objects × 100
  random permutations of the same object must all canonicalize to
  byte-identical output.
- **Sign → Verify round-trip** — generate keypair, sign payload, verify
  passes; flip one byte of signature → verify fails; flip one byte of
  payload → verify fails; flip one byte of pubkey → verify fails.
- **Passphrase handling** — encrypted cosign-boxed key: right
  passphrase decrypts, wrong passphrase returns a typed error
  (`ErrKeyPassphraseIncorrect`), empty passphrase against an encrypted
  key returns `ErrKeyEncrypted`. The stdin byte slice is asserted
  zeroed after return (`require.Equal(make([]byte, len), original)`).
- **Sidecar file emission** — write + load round-trip on a
  `t.TempDir()`. Absent `.rekor.json` when `RekorURL` is empty. Absent
  `.cert` in keyed mode. All three files have mode `0644`.
- **Error typing** — every exported error implements `errs.Categorized`
  and returns `CategoryContent` for signature-related failures,
  `CategoryUser` for key-file-missing.

### 7.2 Unit — `pkg/diff`

- **BundleSpec transport-prefix enforcement** — every bare-path
  permutation of baseline/target fields returns
  `ErrBundleSpecMissingTransport` with `FieldPath` set correctly
  (`pairs[0].baseline`, `pairs[1].target`, etc.).
- **Transport-aware resolveSpecPath** — a BundleSpec using
  `docker-archive:./rel/path.tar` resolves the filesystem path relative
  to the spec file's directory, just like today. A
  `docker://host/repo:tag` value round-trips unchanged.

### 7.3 Integration — `cmd`

Test matrix in `cmd/diff_registry_integration_test.go` +
`cmd/bundle_registry_integration_test.go`:

| Case | Registry mode | Auth | TLS | Expect |
|---|---|---|---|---|
| `diff` anon pull | `registrytest` anonymous | none | HTTP | exit 0, delta smaller than baseline |
| `diff` basic-auth | `registrytest` with htpasswd | `--creds user:pw` | HTTP | exit 0 |
| `diff` bearer token | `registrytest` with token auth | `--registry-token` | HTTP | exit 0 |
| `diff` mTLS | `registrytest` WithTLS + client CA | `--cert-dir` | HTTPS | exit 0 |
| `diff` bad creds | `registrytest` with htpasswd | `--creds wrong:pw` | HTTP | exit 2, hint mentions `--authfile` |
| `diff` unreachable | no listener | — | — | exit 3 |
| `diff` unknown tag | `registrytest` anonymous | — | HTTP | exit 4, "manifest not found" |
| `diff` retry success | `registrytest` WithInjectFault(503×2 → 200) | anon | HTTP | exit 0, retry counter = 2 |
| `diff` retry exhaust | `registrytest` WithInjectFault(503×10) | anon, `--retry-times 2` | HTTP | exit 3, "retries exhausted" |
| `bundle` mixed sources | `registrytest` for svc-a, local archive for svc-b | — | — | exit 0, both images in sidecar |
| `bundle` bare-path BundleSpec | — | — | — | exit 2, sed migration hint in stderr |

Signing test cases in `cmd/sign_integration_test.go` +
`cmd/verify_integration_test.go`:

| Case | Setup | Expect |
|---|---|---|
| Sign + verify OK | key pair fixture, `--sign-key k.priv` then `--verify k.pub` | exit 0 |
| Verify wrong key | signed with k1, verify with k2 | exit 4, "does not verify" |
| Verify unsigned | no `--sign-key`, but consumer passes `--verify k.pub` | exit 4, "archive has no signature" |
| Verify absent on signed | signed archive, no `--verify` flag | exit 0, no warning, `.sig` file ignored |
| Encrypted key passphrase | cosign-boxed key, passphrase via stdin | exit 0 |
| Encrypted key wrong passphrase | cosign-boxed key, wrong passphrase via stdin | exit 2 (user) |
| Tamper sidecar after sign | sign, flip one byte of `diffah.json` in the tar, verify | exit 4 |
| Tamper `.sig` file | sign, flip one byte of `.sig`, verify | exit 4 |
| Rekor roundtrip | `registrytest`-hosted Rekor stub, `--rekor-url` on sign + `--verify-rekor-url` on verify | exit 0, `.rekor.json` bytes stable across repeat runs |

### 7.4 Cosign compat snapshot test

`pkg/signer/cosign_compat_test.go` — gated by `DIFFAH_SIGN_COMPAT=1`
and a `cosign` binary probe. Runs in release-signing CI only.

1. `signer.Sign` a fixed payload with a committed test key, write
   `OUT.sig`.
2. `exec.LookPath("cosign")`; skip if missing.
3. `cosign verify-blob --key pub.pem --signature OUT.sig PAYLOAD-FILE`
   → must exit 0.
4. Reverse: `cosign sign-blob --key priv.pem --output-signature OUT2.sig
   PAYLOAD-FILE`. Our `Verify(OUT2.sig)` must pass.

### 7.5 Bandwidth / memory regression

`pkg/exporter/bandwidth_test.go` — new test using the `registrytest`
harness:

1. Build a 5-layer baseline image in the in-process registry; attach
   an HTTP interceptor counting `GET /v2/.../blobs/sha256:...` per
   blob digest.
2. Run `diffah diff docker://.../v1 docker://.../v2 delta.tar`.
3. Assert: every baseline layer is `GET`'d **exactly once** (required
   for content-similarity fingerprinting — not a regression).
4. Assert via `runtime.MemStats` delta that heap growth stays `< 2 ×
   maxLayerSize × workers`. Threshold committed alongside the
   benchmark.

### 7.6 Dry-run signing probes

Per §8.3 open question #4, dry-run exercises the key-parse path to
catch broken `--sign-key` files early. `cmd/diff_test.go` /
`cmd/bundle_test.go` get:

- `diffah diff --dry-run --sign-key /does/not/exist ...` → exit 2
  (user), hint points at the flag.
- `diffah diff --dry-run --sign-key garbled.pem ...` → exit 2 (user),
  hint mentions PEM parse error.
- `diffah diff --dry-run --sign-key good.key ...` → exit 0; no `.sig`
  file produced (dry-run does not write the archive).

`diffah doctor` is not extended by Phase 3 — there is no runtime
config file yet (deferred to Phase 5), so there is nothing global for
doctor to probe. Trust-root / Rekor-reachability probes slot into
`doctor` when the Phase 5 config file ships (§8.3 open question #1).

### 7.7 What we explicitly *don't* test in Phase 3

- Keyless signing flows (OIDC, Fulcio, mandatory Rekor).
- `--sign-inline` tar-embedded sigs.
- Cross-version sidecar schema migration — schema did not bump.
- Multi-sig / co-signing.
- Third-party Rekor instances beyond `registrytest` stub — a single
  reference stub is sufficient to prove the wire-format; real-world
  Rekor URLs are a user-config concern.

## 8. Risks & open questions

### 8.1 Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Our hand-rolled cosign-format `.sig` drifts from stock `cosign verify-blob` after a cosign 2.x point-release changes the base64 encoding or signature-field framing. | Medium — sigstore has bumped the bundle format twice in 2024. | CI snapshot test (§7.4) runs against the pinned `cosign` version installed by `sigstore-installer` in our release workflow. Breakage is caught at the PR-CI gate, not in users' hands. `.sig` format for keyed mode has been stable since cosign 1.x; we only risk churn if we expand to `.cert`/Rekor-bundle emission, both gated to explicit opt-in in Phase 3. |
| `sigstore/sigstore` (v1.9.5) API drift when we later try to upgrade. | Low-Medium. | Pin the dep version in `go.mod`; the public API we consume is narrow (`ecdsa` keypair + ASN.1 signing — largely stdlib). Upgrade with a planned sweep, covered by the signer round-trip tests. |
| Bandwidth cost of content-similarity matching scales with total baseline size — surprising to users who expect diff to be cheap. | High surprise risk. | `docs/performance.md` explicitly calls out: "diffah diff reads every baseline layer (but does not retain them). For an N-GB baseline set, expect N GB of registry egress per `diff` run." Add a `--dry-run` plan-only path that reports expected bandwidth. |
| Users mistake BundleSpec migration as "Phase 3 broke my build" in CI runs. | Medium. | Migration error includes (a) the sed one-liner, (b) a URL to the Phase 3 CHANGELOG section. `diffah doctor --config ./bundle.json` gains a BundleSpec linter that flags bare paths with the same sed fix. |
| `--sign-key-password-stdin` colliding with other stdin consumers (e.g., piped bundle.json). | Low — we do not accept BundleSpec on stdin. | Documented: `--sign-key-password-stdin` consumes stdin to EOF (or first `\n`). Explicit in `--help`. |
| sigstore public-good Rekor rate-limits or availability outages. | Medium — Rekor has had multi-hour outages historically. | Rekor is opt-in via `--rekor-url`. Default offline posture insulates the default path. When opted in, failures classify as `env/3` (not `content/4`) — the archive is still written locally; only the `.rekor.json` sidecar is absent, and we emit a warning. |
| Leaking customer secret state to a public Rekor when a user sets `--rekor-url` by accident. | Low — must be typed explicitly. | Default is no upload. The `--rekor-url` flag's `--help` text reads: "**Uploads the signature payload digest to a public transparency log. Do not set this unless you have confirmed with your release process that delta identifiers are safe to publish.**" |
| Cosign-boxed key scrypt parameters mismatch between sigstore 1.x and 2.x. | Low — scrypt(N=2^15, r=8, p=1) has been stable. | Test fixtures include both 1.x-boxed and 2.x-boxed encrypted keys; signer rejects unknown KDF params with `ErrKeyUnsupportedKDF`. |
| Phase 3 PR depth triggering review fatigue. | High given scope — registry + signing in one spec. | PR slicing — see §8.2. |

### 8.2 PR slicing (within the one-phase decision)

One spec and one merge train, but **eight** incrementally-mergeable
PRs, each independently green in CI:

1. `exporter.Pair` field rename + internal call-site update. No
   user-visible change.
2. `exporter.Options.SystemContext` wiring (thread `sys` through every
   `NewImageSource`). No user-visible change.
3. `planPair` — swap `imageio.OpenArchiveRef` for
   `alltransports.ParseImageName`. Still path-only at the CLI edge;
   internal swap.
4. CLI: register `installRegistryFlags` on `diff` and `bundle`. Parse
   into `SystemContext`. Docs/examples updated. **User-visible
   feature: registry sources on `diff`/`bundle`.**
5. BundleSpec breaking change: require transport prefixes, emit sed
   migration hint. **User-visible breaking change.**
6. New `pkg/signer` package — standalone with full unit tests. No CLI
   integration yet.
7. CLI: `--sign-key` / `--sign-key-password-stdin` / `--rekor-url` on
   `diff` and `bundle`; wire to signer; integration tests.
   **User-visible feature: signing.**
8. CLI: `--verify` / `--verify-rekor-url` on `apply` and `unbundle`;
   wire to `signer.Verify`; integration tests; cosign-compat snapshot
   test. **User-visible feature: verification.**

### 8.3 Open questions carried out of brainstorming

1. **Config-file integration.** Phase 5's `~/.diffah/config.yaml`
   would presumably let users set `signing.key = /path/to/cosign.key`
   and `verify.public_key = /path/to/cosign.pub` as defaults. Is that
   table-stakes for Phase 3, or does it stay "CLI-flags-only" until
   Phase 5 actually ships the config file? *Current leaning:*
   CLI-flags-only. Phase 5 adds the config surface later.
2. **`--sign-payload <file>` override.** Some users might want to sign
   something other than `jcs(sidecar.json)` — e.g., an SBOM they
   attach. *Current leaning:* do not reserve the flag; add later if
   requested. Keeps the surface minimal.
3. **Signing in the streaming-exporter refactor.** Phase 4 rewrites
   the exporter to stream blobs without buffering. When that ships,
   the "re-read sidecar bytes for canonicalization" step in §4.3 still
   works (we read after the archive is closed). No signing/streaming
   interaction to worry about. Called out in the plan as a
   confirmed-not-blocking item.
4. **Dry-run signing.** `diffah diff --dry-run --sign-key k.priv ...`
   — should dry-run exercise the signer (prove the key parses), or
   skip signing entirely? *Current leaning:* dry-run parses the key
   (fail-fast if the key is broken) but does not compute a signature.
   Zero wall-clock cost, catches key-file typos early.
5. **Inspect integration.** `diffah inspect delta.tar` today reports
   bundle contents and intra-layer backend. Should it also report
   signing status (signed / unsigned / signed-with-Rekor-bundle)?
   *Current leaning:* yes, minor; add a `signing` object in the
   `--output json` shape. Not P0 for the spec but mentioned in the
   plan.

All of these are deferrable past the first Phase 3 PR; none block
landing the core eight PRs above.

## 9. Next steps

1. Review and approve this spec.
2. Invoke the `writing-plans` skill to draft the phased implementation
   plan from the eight-PR slicing above.
3. Execute against the plan, merging each PR as it goes green in CI.
4. On completion, update `CHANGELOG.md`, `docs/compat.md`, and bump
   the roadmap §4 Phase 3 status from "planned" to "delivered."
