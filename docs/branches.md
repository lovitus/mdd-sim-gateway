# Branch Disposition and Release Ref Policy

## Current Repository Ref State

- **Active Development Branch**: `main` is the **sole** active branch on both local checkouts and `origin`. All historical feature, fix, review, and recovery PRs through **PR #48** (`4c3a41a`), **PR #56** (`2f40d0a`), **PR #57**, and **PR #58** (`6236b1a`) have been verified and integrated into `main` (or explicitly superseded/closed per owner decisions).
- **Historical Branch & Tag Offline Archive**: Before pruning 33 merged/obsolete branches, 7 legacy recovery worktrees, and 200+ candidate `archive/*` tags from active Git refs on 2026-11-24/27, a complete offline bundle (`mdd-gateway-all-refs.bundle`) and dirty-worktree snapshots were verified and preserved in the maintainer's private archive directory, and the full historical run-by-run disposition log is retained in [branches-history-2026-09-to-11.md](docs/archive/2026-11-27/branches-history-2026-09-to-11.md).
- **Published Release Tags**:
  - Go runtime & desktop/Linux Agents: `v2.0.0`, `v2.1.0`, `v2.2.0` (plus legacy pre-rewrite tags retained immutable for historical reference).
  - Native Android preview: `android-preview-v2.0.0`, `android-preview-v2.1.0`, `android-preview-v2.2.0` (plus earlier milestone preview tags).

---

## Governance Rules for Subsequent Work

1. **Start From `main`**:
   - Always branch new work from current `origin/main`. Never restore retired Python/Docker/Asterisk/VPCD code, excluded PR #8 server-side pairing/call-recovery expansions, or superseded soft-delete eSIM drafts from historical commits or offline bundles.
2. **Delivery Batch Discipline**:
   - Group backend, protocol, WebUI, tests, and ledger/documentation updates for one user workflow into a single cohesive commit/PR rather than micro-commits.
   - Delete short-lived topic branches immediately upon squash-merge so `origin` stays clean (`main` + immutable release tags only).
3. **Release Tag Integrity**:
   - Never move, delete, or clobber an already-published `v*` or `android-preview-v*` release tag or its uploaded assets.
   - For each new semantic release `X.Y.Z`: update `VERSION`, `README.md`, and `docs/status/acceptance.json` (`node tools/repository-check.mjs --write`), pass `main` CI, tag and publish `android-preview-vX.Y.Z` first, and then tag and publish `vX.Y.Z` on the exact same commit SHA and tree.
4. **Separation of Repository State and Production Deployment**:
   - Merging to `main` or publishing a GitHub Release does **not** imply production fleet deployment or physical carrier/hardware acceptance. Record actual authorized deployments and scoped field observations truthfully in `DEPLOYMENT.md` and `docs/status/acceptance.json`.
