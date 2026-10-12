import assert from 'node:assert/strict'
import { readFile, writeFile } from 'node:fs/promises'
import { execFileSync, spawnSync } from 'node:child_process'

const path = 'webui/src/mdd/views/UnifiedPages.jsx'
const fixed = await readFile(path)
try {
  await writeFile(path,execFileSync('git',['show',`c7ada5bdccd25a97e052531e5f8d5a497a01acc2:${path}`]))
  const result = spawnSync('npm',['run','test:hardware-imei-browser'],{cwd:'webui',encoding:'utf8',timeout:120000,
    env:{...process.env,MDD_BROWSER_EVIDENCE:'browser-evidence/hardware-imei/baseline'}})
  const log = `${result.stdout || ''}${result.stderr || ''}`
  process.stdout.write(log)
  assert.notEqual(result.status,0,'old page must fail invalid-snapshot readback')
  assert.match(log,/AssertionError[^]*INVALID_SNAPSHOT_READBACK/,'must render and reach invalid-readback assertion, not compile or time out')
  console.log('Confirmed invalid HTTP 200 snapshot was displayed as unbound; fixed source restored')
} finally {
  await writeFile(path,fixed)
}
