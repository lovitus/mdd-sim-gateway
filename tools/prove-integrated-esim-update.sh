#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
evidence="$root/integration-proof-evidence"
mkdir -p "$evidence"
file=go-runtime/internal/core/update_maintenance.go
cp "$file" "$evidence/fixed.go"
trap 'cp "$evidence/fixed.go" "$file"' EXIT
git apply --reverse --unidiff-zero <<'PATCH'
diff --git a/go-runtime/internal/core/update_maintenance.go b/go-runtime/internal/core/update_maintenance.go
--- a/go-runtime/internal/core/update_maintenance.go
+++ b/go-runtime/internal/core/update_maintenance.go
@@ -43 +43 @@
-	if runtime.CheckMedia == nil || len(runtime.Checks) == 0 {
+	if runtime.CheckMedia == nil || len(runtime.Checks) == 0 || runtime.Agents == nil || runtime.EUICC == nil {
@@ -45,0 +46,8 @@
+	// Admission is already closed and in-flight writers have left. Reject before
+	// any update drain can block the original eSIM owner's explicit recovery.
+	if err := runtime.EUICC.UpdateBusy(); err != nil {
+		return err
+	}
+	if err := runtime.Agents.HostMaintenanceBusy(); err != nil {
+		return err
+	}
PATCH
set +e
go -C go-runtime test -json -count=1 -timeout 1m -run '^TestUpdateRejectsRetainedESIMWithoutBlockingOriginalRecovery$' ./internal/core >"$evidence/before.jsonl" 2>"$evidence/before.stderr"
status=$?
set -e
test "$status" -ne 0
node --input-type=module - "$evidence/before.jsonl" "$evidence/before.stderr" <<'JS'
import fs from 'node:fs';
const rows = fs.readFileSync(process.argv[2], 'utf8').trim().split('\n').map(line => JSON.parse(line));
const stderr = fs.readFileSync(process.argv[3], 'utf8');
const output = rows.map(row => row.Output || '').join('');
if (/build failed|panic:|test timed out|DATA RACE/.test(output + stderr)) throw Error('Not a behavioral counterexample');
const parent = 'TestUpdateRejectsRetainedESIMWithoutBlockingOriginalRecovery';
for (const name of ['unknown', 'cleanup-only-before-host']) {
  const test = parent + '/' + name;
  if (!rows.some(row => row.Test === test && row.Action === 'fail') ||
      !rows.some(row => row.Test === test && row.Output?.includes('ESIM_UPDATE_INTERLOCK:')))
    throw Error('Missing intended counterexample: ' + test);
}
if (rows.some(row => row.Action === 'fail' && row.Test && row.Test !== parent && !row.Test.startsWith(parent + '/')))
  throw Error('Unexpected failed test');
console.log('Both retained eSIM update interlocks reproduced as compiled behavior failures');
JS
cp "$evidence/fixed.go" "$file"
go -C go-runtime test -race -json -count=1 -timeout 1m -run '^TestUpdateRejectsRetainedESIMWithoutBlockingOriginalRecovery$' ./internal/core >"$evidence/fixed.jsonl" 2>"$evidence/fixed.stderr"
git diff --exit-code -- "$file"
printf '%s\n' 'Fixed original-recovery and next-update paths pass under race detection'
