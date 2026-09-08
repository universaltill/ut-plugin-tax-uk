# Code review: auto-tag-release.yml rollout (ut-docs#1700)

**Date:** 2026-09-08
**Card:** ut-docs#1700 (rollout of ut-docs#1694's workflow to remaining `ut-plugin-*` repos)
**Author:** scrum-master pipeline (cloud cycle, `lane:cloud-41`), on behalf of Pouria Teimouri

## What changed

Added `.github/workflows/auto-tag-release.yml`, copied byte-for-byte from
the canonical, independently-reviewed copy in `ut-plugin-tax-de`
(`docs/code-reviews/2026-09-07-auto-tag-release-workflow-1694.md` there —
full design rationale, recursion-guard analysis and behavioral test
evidence live in that record and are not repeated here per ut-docs#1700's
own instructions; this record only covers what is repo-specific).

## Repo-specific verification

- Confirmed `.github/workflows/release.yml` is tag-triggered
  (`on: push: tags: ["v*"]`) and declares `workflow_dispatch` inputs named
  `channel` (choice, includes `stable`) and `publish` (boolean) — matching
  what the copied workflow's dispatch step passes, with no adaptation
  needed.
- `diff` against the canonical `ut-plugin-tax-de` copy: byte-identical.
- **This repo has never been tagged at all**: `manifest.json`'s `version`
  is `1.0.0` and `git tag -l` returns nothing. Merging this workflow is
  expected to create the first tag, `v1.0.0`, and dispatch a real
  `release.yml` run on the first push to `main` that carries it — this is
  this card's "live drift to prove against" case (no tag exists yet, which
  is the maximal-drift instance of the same gap). DevOps must verify the
  tag and release run actually happened rather than trust the YAML.
- `manifest.json` confirmed at repo root (script's assumption).

## Independent review

Mechanically identical to the already-reviewed canonical file (full
independent Opus review on the original: ut-plugin-tax-de's own
2026-09-07 record). This repo's own diff is a verbatim copy plus this
review record — verdict: **SAFE TO MERGE**, no repo-specific deviation
found.
