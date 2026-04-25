# Security policy

## Reporting a vulnerability

If you believe you have found a security vulnerability in `diffah`,
please **do not** open a public GitHub issue, post on a public forum,
or send a public pull request.

Instead, report it privately via either of the following channels:

1. **GitHub Security Advisories** — preferred. Open a private advisory
   at <https://github.com/leosocy/diffah/security/advisories/new>.
2. **Email the maintainer** at `leosocy@gmail.com`.

Please include:

- A description of the vulnerability and the impact you believe it
  has.
- The diffah version (`diffah version`) and platform you observed it
  on.
- A minimal reproduction — ideally a delta archive or a fixture
  command line that triggers the issue.
- Whether you intend to disclose publicly, and on what timeline.

You should expect:

- An acknowledgement within **3 working days**.
- An initial triage decision (accepted / needs more info / out of
  scope) within **7 working days**.
- A fix or mitigation timeline communicated within **30 days** of the
  initial triage. Severity ranking follows
  [CVSS v4.0](https://www.first.org/cvss/v4-0/).

## Scope

In scope:

- Anything reachable through the `diffah` binary or the `pkg/...`
  Go API.
- Sidecar / archive parsing — malformed archives that cause panics,
  resource exhaustion, or unexpected filesystem writes.
- Signature handling — bypasses of the signature-verification matrix
  documented in [`docs/compat.md`](docs/compat.md), forgery, or
  bypass of `--verify`.
- Registry / transport handling — credential leakage, TLS bypass,
  request smuggling against `go.podman.io/image/v5` callers.
- Build / release supply chain — issues in
  `.github/workflows/release.yml`, `.goreleaser.yaml`, the cosign
  signing path, or the published container image.

Out of scope (please file regular issues for these):

- Behavioral bugs that don't have a security impact.
- Vulnerabilities exclusively in upstream dependencies — please report
  those upstream first; we will follow up with a coordinated fix.
- Issues in `internal/...` that are unreachable from the public CLI
  or library surface.

## Verifying release artifacts

Release archives published to
<https://github.com/leosocy/diffah/releases> are signed with `cosign`
using GitHub OIDC keyless / Sigstore. To verify a release:

```sh
cosign verify-blob \
  --certificate-identity-regexp 'https://github.com/leosocy/diffah/.+' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --signature diffah_<VERSION>_linux_amd64.tar.gz.sig \
  --certificate diffah_<VERSION>_linux_amd64.tar.gz.pem \
  diffah_<VERSION>_linux_amd64.tar.gz
```

The container image at `ghcr.io/leosocy/diffah:<VERSION>` is signed
the same way:

```sh
cosign verify \
  --certificate-identity-regexp 'https://github.com/leosocy/diffah/.+' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/leosocy/diffah:<VERSION>
```

SBOMs (CycloneDX) for every release artifact are published as
`*.sbom.json` next to the artifact on the releases page.

## Versioning and supported releases

`diffah` is pre-`v1.0.0`. Until `v1.0.0`, security fixes ship on the
**latest minor release** only. Older minor versions are not
backported. A formal long-term-support policy will be published at
`v1.0.0`.
