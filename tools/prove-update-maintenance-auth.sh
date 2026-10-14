#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
evidence="$root/update-auth-proof"
mkdir -p "$evidence"
file="$root/go-runtime/cmd/mdd-updater/main.go"
cp "$file" "$evidence/fixed.go"
trap 'cp "$evidence/fixed.go" "$file"' EXIT
git apply --unidiff-zero <<'PATCH'
diff --git a/go-runtime/cmd/mdd-updater/main.go b/go-runtime/cmd/mdd-updater/main.go
--- a/go-runtime/cmd/mdd-updater/main.go
+++ b/go-runtime/cmd/mdd-updater/main.go
@@ -293 +293 @@
-	request.Header.Set("X-MDD-Provider-Apply-Token", core.token)
+	request.Header.Set("Authorization", "Bearer "+core.token)
PATCH
set +e
go -C "$root/go-runtime" test -json -count=1 -timeout 2m ./cmd/mdd-updater -run '^TestUpdaterUsesProductionMaintenanceAuthentication$' > "$evidence/before.jsonl" 2> "$evidence/before.stderr"
result=$?
set -e
if [[ "$result" == 0 ]]; then
  echo 'Old maintenance client unexpectedly passed production authentication' >&2
  exit 1
fi
node --input-type=module - "$evidence" <<'NODE'
import fs from 'node:fs';
const dir = process.argv[2];
const records = fs.readFileSync(`${dir}/before.jsonl`, 'utf8').trim().split('\n').map(line => JSON.parse(line));
const stderr = fs.readFileSync(`${dir}/before.stderr`, 'utf8');
const outputs = records.map(row => row.Output || '').join('') + stderr;
if (/build failed|undefined:|panic:|test timed out|DATA RACE/i.test(outputs)) throw Error('Not a compiled behavioral counterexample');
const prefix = 'TestUpdaterUsesProductionMaintenanceAuthentication';
const expected = [`${prefix}/valid/get`, `${prefix}/valid/post`];
for (const test of expected) {
  if (!records.some(row => row.Action === 'fail' && row.Test === test)) throw Error(`Missing intended fail: ${test}`);
  if (!records.some(row => row.Test === test && row.Output?.includes('PRODUCTION_MAINTENANCE_AUTH:'))) throw Error(`Missing authentication assertion: ${test}`);
}
const allowed = new Set([...expected, `${prefix}/valid`, prefix]);
if (records.some(row => row.Action === 'fail' && row.Test && !allowed.has(row.Test))) throw Error('Unexpected failure');
console.log('Both old valid-credential GET/POST calls failed through production authentication');
NODE
cp "$evidence/fixed.go" "$file"
go -C "$root/go-runtime" test -race -json -count=1 -timeout 2m ./cmd/mdd-updater -run '^TestUpdater(UsesProductionMaintenanceAuthentication|ReconcilesOriginalMaintenanceWithCorruptWorkerStatus|ReconcilesOnlyOriginalUnactivatedRequest)$' > "$evidence/fixed.jsonl" 2> "$evidence/fixed.stderr"
git diff --exit-code -- go-runtime/cmd/mdd-updater/main.go
