# Integration and audit branches

`main` is the product integration branch. A temporary review branch is not another product runtime and is not
required to be merged wholesale. Verify the exact source diff and CI before merging production changes.

At baseline `3e6d5db` (2026-09-20):

| Branch | Disposition |
|---|---|
| codex/review-prack-fix-7f2e129 @ 29dfde8 | Production changes merged by PR #2; eligible for ordinary post-merge branch cleanup. |
| codex/review-main-7f2e129 @ 39f7a7f | Retained audit-only workflow for pinned HTTP-500 counterexamples. Not a missing product fix. |
| codex/review-postmerge-7f2e129 @ 910de2f | Retained negative controls/proof harness. Its tested product patch was published separately and merged. |

The September 20 dependency-audit preparation on the last branch adds a temporary dependency archive workflow;
it does not change main's runtime. Do not merge audit patch-injection/publishing harnesses into production simply to
make branch divergence zero. Evidence is also retained in versioned review reports and linked Actions runs.
Local working-tree lag belongs to the particular machine; it cannot be inferred from remote branch names.
Before deletion, preserve any unique evidence and check the exact head and open PRs. This cleanup does not delete
unmerged audit evidence or other developers' local branches. Short-lived PR branches remain permitted.
