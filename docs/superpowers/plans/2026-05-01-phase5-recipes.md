# Phase 5.6 — Recipes cookbook implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a four-recipe cookbook under `docs/recipes/`, link it from the README, and back each recipe with a shell-based smoke test that runs in CI. Closes the last open item of Phase 5 per the production-readiness roadmap (`docs/superpowers/specs/2026-04-23-production-readiness-roadmap-design.md` §"Phase 5 — DX & diagnostics polish", lines ~166–180; smoke-test exit criterion at §6.2 line 274).

**Architecture:**

- **Recipes live in `docs/recipes/`** as one Markdown file per recipe (`<slug>.md`) plus an `index.md` that the main `README.md` "Recipes" section links to. Each recipe follows a fixed template (Goal → Prerequisites → Setup → Steps → Verify → Cleanup → Variations).
- **Smoke tests live in `scripts/smoke-recipes/<slug>.sh`** as standalone bash scripts. Each script reads the diffah binary path and any required fixture/registry endpoints from environment variables and exits non-zero on failure.
- **Smoke driver is a Go integration test** at `cmd/recipes_smoke_integration_test.go` (build tag `integration`). It builds the binary once, spins up the needed `registrytest.Server` instances per recipe, seeds OCI fixtures, exports envvars, and exec's each shell script via `bash`. This satisfies the spec's "shell-based assertions" while reusing the existing fixture harness — docs and smokes can't drift because the script *is* what runs.
- **Registry-to-registry mirror** uses two `registrytest.Server` instances (source + mirror); **air-gapped** uses zero registries (docker-archive transport only); **CI delta release** and **offline verification** use one.

**Tech stack:** Go 1.25 (Go integration test driver), `internal/registrytest` (in-process registry), `cmd/testdata/fixtures/v1_oci.tar` / `v2_oci.tar` (existing OCI fixtures), `bash` (smoke scripts), `cosign` is NOT introduced — the offline-verification recipe uses `diffah verify` against signatures produced by `diffah sign` (already shipped in Phase 3).

**Spec reference:** `docs/superpowers/specs/2026-04-23-production-readiness-roadmap-design.md` §"Phase 5 — DX & diagnostics polish".

**Brainstorm decisions:**

- **D1 — Recipe template (fixed shape).** Every recipe has the seven sections in order: `## Goal`, `## When to use`, `## Prerequisites`, `## Setup`, `## Steps`, `## Verify`, `## Cleanup` (optional), `## Variations` (optional), `## Troubleshooting` (optional). Consistency lets readers skim and lets smoke scripts mirror the `Steps` section line-for-line.
- **D2 — Recipe slugs and ordering.** `airgap-delivery.md`, `registry-mirror.md`, `ci-delta-release.md`, `offline-verify.md`. README "Recipes" section lists them in increasing setup complexity (CI → offline-verify → airgap → registry-mirror).
- **D3 — Smoke-script contract.** Each `scripts/smoke-recipes/<slug>.sh` accepts these envvars: `DIFFAH_BIN`, `WORK_DIR` (writable scratch), and recipe-specific endpoints (`SOURCE_REGISTRY` / `MIRROR_REGISTRY` / `BASELINE_OCI_TAR` / `TARGET_OCI_TAR`). Scripts use `set -euo pipefail`, log every command via `set -x`, and exit 0 on pass.
- **D4 — Smoke driver placement.** `cmd/recipes_smoke_integration_test.go` (one Go test file, four `t.Run` subtests). Reuses `findRepoRoot`, `integrationBinary`, `runDiffahBin`, `seedOCIIntoRegistry` already in `cmd/integration_main_test.go`. New helper `execSmokeScript(t, scriptPath, env)` wraps `exec.Command("bash", scriptPath)`.
- **D5 — Registry-mirror recipe semantics.** "Nightly registry-to-registry mirror" means: nightly cron runs `diffah diff` between yesterday's tag and today's tag in the source registry, then `diffah apply` writes the reconstructed image directly to the mirror registry. The delta archive is the only thing that crosses the WAN. Smoke proves this end-to-end with two in-process registries.
- **D6 — Air-gapped recipe scope.** Producer side and customer side both use `docker-archive:` (or `oci-archive:`) transports — no registries involved. The "sneakernet" step in the recipe is illustrative (instructions for `tar`/`scp`/USB); the smoke proves only the diffah half (`diff` produces a delta from two archives, `apply` reconstructs from delta + baseline archive).
- **D7 — Offline-verification recipe scope.** Signing happens at archive-creation time via `diffah diff --sign-key <PEM>` (produces a `.sig` sidecar next to the archive). Verification happens at apply time via `diffah apply --verify <pub-key>`. There is no separate `diffah sign` / `diffah verify` command. The recipe documents the static-key flow (PEM-on-disk EC P256); the smoke reuses the existing `pkg/signer/testdata/test_ec_p256.key` test fixture and its corresponding public key. **No cosign dependency** — cosign URIs (`cosign://...`) are reserved and return exit 2 today, so the recipe explicitly notes "static-key signing only; cosign / KMS integration is future work."
- **D8 — CI-delta-release recipe scope.** Demonstrates a GitHub Actions workflow snippet that runs `diffah diff` between two registry tags and uploads the delta as a release artifact. Recipe includes a complete (~30-line) `.github/workflows/delta-release.yml` snippet readers can copy. Smoke runs only the diffah half (the upload step is a `gh release upload` shell command, illustrated but not tested — out of scope).
- **D9 — README integration.** Insert a new `## Recipes` section between the existing `## Quick start` and `## Image references` sections. Section is an unordered list with one bullet per recipe (title + one-sentence summary + relative link to `docs/recipes/<slug>.md`). The `docs/recipes/index.md` mirrors the same content for users browsing the docs/ tree directly.
- **D10 — CI wiring.** Smoke driver runs under the existing `integration` build tag — no new workflow file. The current `.github/workflows/integration.yml` already invokes `go test -tags integration ... ./...` which will pick up `cmd/recipes_smoke_integration_test.go` automatically. No CI config changes needed.

**Out of scope (per spec §"Out of scope" and Phase 5 anti-goals):**

- No new flags or commands (recipes consume what's already shipped).
- No multi-arch publishing recipes (P5.5 territory).
- No Homebrew install recipes (P5.4 territory).
- No real-world secret management or production cron-job templates beyond the snippet shown.
- No upload step in CI-delta-release smoke (`gh release upload` is illustrative only).

---

## File plan

| File | Action | Responsibility |
|---|---|---|
| `docs/recipes/index.md` | create | Index page listing the four recipes with one-line summaries |
| `docs/recipes/airgap-delivery.md` | create | Air-gapped customer delivery walkthrough |
| `docs/recipes/registry-mirror.md` | create | Nightly registry-to-registry mirror walkthrough |
| `docs/recipes/ci-delta-release.md` | create | CI-driven delta release workflow walkthrough |
| `docs/recipes/offline-verify.md` | create | Offline signature verification walkthrough |
| `scripts/smoke-recipes/airgap-delivery.sh` | create | Bash smoke for the airgap recipe |
| `scripts/smoke-recipes/registry-mirror.sh` | create | Bash smoke for the registry-mirror recipe |
| `scripts/smoke-recipes/ci-delta-release.sh` | create | Bash smoke for the ci-delta-release recipe |
| `scripts/smoke-recipes/offline-verify.sh` | create | Bash smoke for the offline-verify recipe |
| `cmd/recipes_smoke_integration_test.go` | create | Go integration driver — sets up env, exec's each script |
| `README.md` | modify | Add new `## Recipes` section linking to `docs/recipes/*.md` |
| `CHANGELOG.md` | modify | Phase 5.6 entry under `[Unreleased]` |

---

## Phase 1 — Recipe template + first recipe (CI-driven delta release)

CI delta release is the simplest of the four (single registry, no signing, no sneakernet). Land it first to lock the recipe shape and the smoke-driver pattern, then the other three slot in.

### Task 1: Lock the recipe template by writing `docs/recipes/index.md`

**Files:**
- Create: `docs/recipes/index.md`

- [ ] **Step 1: Write index.md** with the seven-section template description at the top, then a stub list of the four recipes (links resolve later as files land).
- [ ] **Step 2:** Confirm rendering on GitHub via local preview (`grip` or just visual scan).

### Task 2: Write `docs/recipes/ci-delta-release.md`

**Files:**
- Create: `docs/recipes/ci-delta-release.md`

- [ ] **Step 1:** Goal/When-to-use/Prerequisites sections. Prereqs: a registry (GHCR/Harbor/Docker Hub), `diffah` installed, `gh` CLI for the upload step.
- [ ] **Step 2:** Setup section — env vars (`SOURCE_REGISTRY`, `OLD_TAG`, `NEW_TAG`).
- [ ] **Step 3:** Steps section — three numbered shell blocks: (1) `diffah diff docker://...:OLD docker://...:NEW ./delta-NEW.tar`, (2) `diffah inspect ./delta-NEW.tar` to surface savings, (3) `gh release upload vNEW ./delta-NEW.tar` (illustrative).
- [ ] **Step 4:** Verify section — re-apply the delta against the OLD tag and assert the resulting image's manifest digest matches NEW.
- [ ] **Step 5:** Variations section — appendix `.github/workflows/delta-release.yml` snippet.

### Task 3: Write `scripts/smoke-recipes/ci-delta-release.sh`

**Files:**
- Create: `scripts/smoke-recipes/ci-delta-release.sh`

- [ ] **Step 1:** `#!/usr/bin/env bash` + `set -euo pipefail`.
- [ ] **Step 2:** Validate required envvars (`DIFFAH_BIN`, `WORK_DIR`, `SOURCE_REGISTRY`).
- [ ] **Step 3:** Mirror the recipe's diff/inspect/apply commands, substituting envvars for the registry endpoint and using fixture tags `fixtures/v1` / `fixtures/v2` (which the smoke driver seeds).
- [ ] **Step 4:** Verify section: assert the reconstructed image digest matches the seeded `fixtures/v2` digest.
- [ ] **Step 5:** `chmod +x scripts/smoke-recipes/ci-delta-release.sh`.

### Task 4: Add the smoke driver `cmd/recipes_smoke_integration_test.go`

**Files:**
- Create: `cmd/recipes_smoke_integration_test.go`

- [ ] **Step 1:** Build tag `//go:build integration`, package `cmd_test`.
- [ ] **Step 2:** `execSmokeScript(t, scriptPath string, env []string) (stdout, stderr string, exit int)` helper wrapping `exec.Command("bash", scriptPath)`.
- [ ] **Step 3:** `TestRecipeSmoke_CIDeltaRelease` — `findRepoRoot`, `integrationBinary`, spin one `registrytest.Server`, `seedV1V2`, set envvars, run `scripts/smoke-recipes/ci-delta-release.sh`, assert exit 0 + non-empty delta artifact.

### Task 5: Verify Phase 1 end-to-end

- [ ] **Step 1:** `go test -tags integration ./cmd/ -run TestRecipeSmoke_CIDeltaRelease -v`. Expected: PASS.
- [ ] **Step 2:** Spot-check `docs/recipes/ci-delta-release.md` rendering on a local Markdown viewer.
- [ ] **Step 3:** Commit Phase 1 work as one atomic commit: `feat(recipes): CI-driven delta release recipe + smoke (Phase 5.6)`.

---

## Phase 2 — Air-gapped delivery + offline verification

Both recipes are single-fixture (no two-registry setup), so they ship together.

### Task 6: Write `docs/recipes/airgap-delivery.md`

**Files:**
- Create: `docs/recipes/airgap-delivery.md`

- [ ] **Step 1:** Goal/When-to-use sections — emphasize the *only* artifact crossing the air-gap is `delta.tar`.
- [ ] **Step 2:** Prerequisites — producer-side has registry + diffah; customer-side has the baseline image already (as `docker-archive:` or in their local registry) + diffah.
- [ ] **Step 3:** Setup section — establish baseline transports and the `delta.tar` filename convention.
- [ ] **Step 4:** Steps section — three blocks: (1) producer computes `diffah diff baseline target ./delta.tar`, (2) sneakernet (illustrative — `tar`/`scp`/USB), (3) customer applies `diffah apply ./delta.tar baseline output`.
- [ ] **Step 5:** Verify section — `diffah inspect` on the customer side to confirm the delta's recorded baseline matches what they have.
- [ ] **Step 6:** Variations — show the same recipe with `oci-archive:` and with a registry-resident baseline on the customer side.

### Task 7: Write `scripts/smoke-recipes/airgap-delivery.sh`

**Files:**
- Create: `scripts/smoke-recipes/airgap-delivery.sh`

- [ ] **Step 1:** Validate envvars `DIFFAH_BIN`, `WORK_DIR`, `BASELINE_OCI_TAR`, `TARGET_OCI_TAR`.
- [ ] **Step 2:** Producer step — `diffah diff docker-archive:$BASELINE_OCI_TAR docker-archive:$TARGET_OCI_TAR $WORK_DIR/delta.tar`.
- [ ] **Step 3:** "Sneakernet" — `cp $WORK_DIR/delta.tar $WORK_DIR/customer/delta.tar`.
- [ ] **Step 4:** Customer step — `diffah apply $WORK_DIR/customer/delta.tar docker-archive:$BASELINE_OCI_TAR oci-archive:$WORK_DIR/customer/restored.tar`.
- [ ] **Step 5:** Verify — `diffah inspect $WORK_DIR/customer/delta.tar` exits 0; `restored.tar` exists with non-zero size.

### Task 8: Write `docs/recipes/offline-verify.md`

**Files:**
- Create: `docs/recipes/offline-verify.md`

- [ ] **Step 1:** Goal/When-to-use — assert delta provenance with a static EC P256 key; no transparency log, no external KMS.
- [ ] **Step 2:** Prerequisites — an EC P256 key pair (private PEM + public PEM); `diffah` ≥ Phase 3.
- [ ] **Step 3:** Setup — show how to generate the key pair with `openssl ecparam -genkey -name prime256v1 -noout -out priv.pem` + `openssl ec -in priv.pem -pubout -out pub.pem`.
- [ ] **Step 4:** Steps — producer signs at diff time: `diffah diff --sign-key priv.pem baseline target ./delta.tar` (writes `delta.tar.sig` sidecar). Consumer verifies at apply time: `diffah apply --verify pub.pem ./delta.tar baseline output`. Note the sidecar travels with the delta; document the .sig naming convention.
- [ ] **Step 5:** Verify — show the expected pass output (apply succeeds with `verified=true` log line) and the failure modes' exit codes (mismatched key → non-zero; tampered archive → non-zero; signature-required-but-missing → exit 4 per `verify_integration_test.go:193`).
- [ ] **Step 6:** Troubleshooting section — common failures (key mismatch, tampered delta, missing signature file, cosign URI rejected as reserved).
- [ ] **Step 7:** Note in a callout: "cosign URIs (`cosign://...`) on `--sign-key` / `--verify` are reserved for future KMS integration and currently return exit 2."

### Task 9: Write `scripts/smoke-recipes/offline-verify.sh`

**Files:**
- Create: `scripts/smoke-recipes/offline-verify.sh`

- [ ] **Step 1:** Validate envvars `DIFFAH_BIN`, `WORK_DIR`, `BASELINE_OCI_TAR`, `TARGET_OCI_TAR`, `SIGN_KEY_PEM`, `VERIFY_KEY_PEM`. Driver passes `pkg/signer/testdata/test_ec_p256.key` and its derived public key for the static-key path (or generates a fresh pair via `openssl` and skips the test if `openssl` is missing).
- [ ] **Step 2:** Producer step — `diffah diff --sign-key $SIGN_KEY_PEM docker-archive:$BASELINE_OCI_TAR docker-archive:$TARGET_OCI_TAR $WORK_DIR/delta.tar`. Assert `$WORK_DIR/delta.tar.sig` exists.
- [ ] **Step 3:** Consumer happy-path — `diffah apply --verify $VERIFY_KEY_PEM $WORK_DIR/delta.tar docker-archive:$BASELINE_OCI_TAR oci-archive:$WORK_DIR/restored.tar`. Assert exit 0 and `restored.tar` non-empty.
- [ ] **Step 4:** Negative test — tamper one byte of `$WORK_DIR/delta.tar`, re-run apply with `--verify`, assert non-zero exit and stderr mentions signature mismatch.

### Task 10: Extend `cmd/recipes_smoke_integration_test.go` with the two new subtests

**Files:**
- Modify: `cmd/recipes_smoke_integration_test.go`

- [ ] **Step 1:** `TestRecipeSmoke_AirgapDelivery` — no registry needed; envvars point to `testdata/fixtures/v1_oci.tar` / `v2_oci.tar`.
- [ ] **Step 2:** `TestRecipeSmoke_OfflineVerify` — derive the public key once (via `openssl ec -pubout` over `pkg/signer/testdata/test_ec_p256.key`, written into `t.TempDir()`); skip with `t.Skip` if `openssl` is not on `$PATH`. Pass `SIGN_KEY_PEM` and `VERIFY_KEY_PEM` to the script.
- [ ] **Step 3:** Run `go test -tags integration ./cmd/ -run TestRecipeSmoke -v`. All three (CI, airgap, offline-verify) PASS.
- [ ] **Step 4:** Commit Phase 2 as `feat(recipes): air-gapped delivery + offline verification recipes (Phase 5.6)`.

---

## Phase 3 — Registry-to-registry mirror

The most setup-heavy recipe — needs two registries, source-side seeding, and apply-into-mirror plumbing.

### Task 11: Write `docs/recipes/registry-mirror.md`

**Files:**
- Create: `docs/recipes/registry-mirror.md`

- [ ] **Step 1:** Goal/When-to-use — reduce nightly egress; only the delta crosses the WAN, not the full v2 image.
- [ ] **Step 2:** Prerequisites — source registry credentials, mirror registry credentials, both reachable from the diff host.
- [ ] **Step 3:** Setup — env vars for both registries; show `--authfile` usage if the two registries need different credentials.
- [ ] **Step 4:** Steps — `diffah diff source/v1 source/v2 ./delta.tar` then `diffah apply ./delta.tar source/v1 mirror/v2` (apply pushes to mirror directly).
- [ ] **Step 5:** Verify — `crane manifest mirror/v2` (or `diffah inspect`) confirms the manifest digest matches `source/v2`.
- [ ] **Step 6:** Variations — multi-image bundle equivalent (`diffah bundle` + `diffah unbundle`).

### Task 12: Write `scripts/smoke-recipes/registry-mirror.sh`

**Files:**
- Create: `scripts/smoke-recipes/registry-mirror.sh`

- [ ] **Step 1:** Validate envvars `DIFFAH_BIN`, `WORK_DIR`, `SOURCE_REGISTRY`, `MIRROR_REGISTRY`.
- [ ] **Step 2:** `diffah diff docker://$SOURCE_REGISTRY/fixtures/v1 docker://$SOURCE_REGISTRY/fixtures/v2 $WORK_DIR/delta.tar --tls-verify=false`.
- [ ] **Step 3:** `diffah apply $WORK_DIR/delta.tar docker://$SOURCE_REGISTRY/fixtures/v1 docker://$MIRROR_REGISTRY/fixtures/v2 --tls-verify=false`.
- [ ] **Step 4:** Verify — pull the manifest from mirror with `diffah inspect`-equivalent shell or by re-pulling and diffing digests against source.

### Task 13: Extend smoke driver with mirror subtest

**Files:**
- Modify: `cmd/recipes_smoke_integration_test.go`

- [ ] **Step 1:** `TestRecipeSmoke_RegistryMirror` — spin TWO `registrytest.Server`s, `seedV1V2(t, source, root)`, leave mirror empty.
- [ ] **Step 2:** Set envvars `SOURCE_REGISTRY` / `MIRROR_REGISTRY` to the two server hosts.
- [ ] **Step 3:** Run `scripts/smoke-recipes/registry-mirror.sh`, assert exit 0.
- [ ] **Step 4:** Post-condition — query the mirror via `crane`-style helper or by spawning `diffah inspect` against `docker://mirror/fixtures/v2`; assert the manifest digest matches the seeded source v2.

### Task 14: Phase 3 verification

- [ ] **Step 1:** `go test -tags integration ./cmd/ -run TestRecipeSmoke -v`. All four PASS.
- [ ] **Step 2:** Commit as `feat(recipes): registry-to-registry mirror recipe + smoke (Phase 5.6)`.

---

## Phase 4 — README integration + CHANGELOG + final review

### Task 15: Wire the README "Recipes" section

**Files:**
- Modify: `README.md`

- [ ] **Step 1:** Insert `## Recipes` section between `## Quick start` and `## Image references`.
- [ ] **Step 2:** Section body: a short prose intro plus an unordered list — one bullet per recipe with title, one-sentence summary, link to `docs/recipes/<slug>.md`.
- [ ] **Step 3:** Cross-link from `docs/recipes/index.md` back to README's "Recipes" section.

### Task 16: CHANGELOG entry

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1:** Under `[Unreleased] — Phase 5: DX & diagnostics polish` add a new bullet: "**Recipes cookbook** (`docs/recipes/*.md`): four end-to-end walkthroughs (air-gapped delivery, registry-to-registry mirror, CI-driven delta release, offline signature verification), each backed by a CI smoke test."

### Task 17: Quality gate

- [ ] **Step 1:** `go build ./...` — PASS.
- [ ] **Step 2:** `golangci-lint run` — PASS (only the smoke driver is new Go code; minimal lint surface).
- [ ] **Step 3:** `go test -tags integration ./cmd/ -run TestRecipeSmoke -count=1 -v` — all four PASS.
- [ ] **Step 4:** `go test ./...` (unit only, no integration tag) — unaffected, all PASS.
- [ ] **Step 5:** Visually skim each recipe markdown for typos, broken links, and code-block fence consistency.

### Task 18: Open PR

- [ ] **Step 1:** Push `chore/phase5-hygiene-recipes` → `origin`.
- [ ] **Step 2:** `gh pr create` with title `feat(recipes): Phase 5.6 cookbook — four recipes + CI smokes` and body that summarizes the four recipes, the smoke-driver pattern, and links the spec.
- [ ] **Step 3:** `gh pr checks --watch` — wait for CI green.
- [ ] **Step 4:** Squash-merge after review.

---

## Open questions

- **Q1 (resolved) — Verify command shape.** No top-level `diffah verify`. Signing is `diffah diff --sign-key <PEM>` (writes `.sig` sidecar). Verification is `diffah apply --verify <pub-PEM>`. Exit codes per `cmd/verify_integration_test.go`: 0 on success, non-zero on key mismatch, 4 when `--verify` is set against an unsigned archive.
- **Q2 (resolved) — No cosign dependency.** Signing uses static EC P256 PEM keys; cosign URIs (`cosign://`) are reserved and currently return exit 2. The smoke uses `pkg/signer/testdata/test_ec_p256.key` plus an `openssl`-derived public key (skip with `t.Skip` if `openssl` is missing).
- **Q3 — Multi-image bundle smoke?** The registry-mirror recipe's "Variations" section mentions `diffah bundle`/`diffah unbundle`. Default: prose only (single-image is representative; bundle is one-flag-different). Reconsider only if a reviewer pushes back during PR review.
