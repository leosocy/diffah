# Recipes

End-to-end walkthroughs for common `diffah` workflows. Each recipe is a
single-file shell narrative — copy, paste, fill in your registry / paths,
and run. Every recipe is also covered by a CI smoke test, so the
commands you read here are the same commands the project itself
exercises on every push.

## Recipe shape

Every recipe follows the same sections so you can skim:

- **Goal** — what the recipe accomplishes in one sentence.
- **When to use** — the real-world situation that motivates it.
- **Prerequisites** — tooling, accounts, files you need before you start.
- **Setup** — environment variables and one-time scaffolding.
- **Steps** — the numbered shell blocks you actually run.
- **Verify** — how to check the outcome before you ship it downstream.
- **Cleanup** *(optional)* — undoing temporary state.
- **Variations** *(optional)* — common forks of the recipe.
- **Troubleshooting** *(optional)* — failure modes and their fixes.

## The cookbook

Listed in roughly increasing setup complexity:

1. [CI-driven delta release](ci-delta-release.md) — `diffah diff`
   between two registry tags inside a CI workflow, then publish the
   delta as a release artifact for downstream apply.
2. [Offline signature verification](offline-verify.md) — sign deltas
   at production time with a static EC P256 key, verify on the consumer
   side without any external KMS or transparency log.
3. [Air-gapped customer delivery](airgap-delivery.md) — produce a
   delta on a connected machine, sneakernet `delta.tar` to an
   isolated environment, and reconstruct the new image there from
   the customer's existing baseline.
4. [Registry-to-registry mirror](registry-mirror.md) — nightly cron
   that diffs a source registry's latest tag and applies the delta
   directly into a mirror registry, so only the delta crosses the
   WAN instead of the full image.

> The README has a top-level [Recipes](../../README.md#recipes) link
> back to this index. If you're contributing a new recipe, copy one of
> the existing files as a template and add a smoke under
> [`scripts/smoke-recipes/`](../../scripts/smoke-recipes/).
