# Repository work and acceptance

Read docs/status/README.md and acceptance.json before changing scope. They are the versioned acceptance ledger;
root TODO summaries are generated, and docs/archive plus archive are historical only. Machine-local instructions
and private progress notes are not project completion evidence and must not override the code or this ledger.

Use one authoritative owner per credential, hardware attachment, paid operation and call lifetime. Preserve exact
SIM/Agent/equipment/generation identity, unknown outcomes, active-call/maintenance guards, disabled rekey choices
and carrier holdoffs. Do not restore Python/VPCD/Asterisk, duplicate UI/call owners, automatic paid retries or
process-wide restarts to hide a missing state transition.

Implement fixes with deterministic pre-fix counterexamples where possible. Run DEVELOPMENT.md checks, including
production-graph/context tests and full Linux Core race tests; inspect skip reasons and final CI head/tree. Never
count a build, HTTP response, saved configuration, process presence or simulator as actual deployed hardware acceptance.

Public records must be bounded and redacted. No live SIM mutation, paid SMS/call or disruptive deployment without
specific authorization. eSIM custom deletion remains an explicitly last, interactive workstream. Do not clean unknown
files, devices, processes or branches. Reference preserved audit commits before disposing of branch names.

Update the ledger with scope, source/test paths, evidence and limitations; regenerate its summaries with
`node tools/repository-check.mjs --write`. A product gap or hardware acceptance item remains open even when a related
cleanup PR is green. A finite review cannot certify the absence of all functional flaws.
