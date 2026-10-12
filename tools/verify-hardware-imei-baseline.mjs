import assert from 'node:assert/strict'
import { readFile, writeFile } from 'node:fs/promises'
import { execFileSync, spawnSync } from 'node:child_process'

const path = 'webui/src/mdd/views/UnifiedPages.jsx'
const fixed = await readFile(path)
try {
  await writeFile(path,execFileSync('git',['show',`ef0d37d2c09a7584aad1f2305ceddce5f336849a:${path}`]))
  const result = spawnSync('npm',['run','test:hardware-imei-browser'],{cwd:'webui',encoding:'utf8',timeout:120000,
    env:{...process.env,MDD_BROWSER_EVIDENCE:'browser-evidence/hardware-imei/baseline'}})
  const log = `${result.stdout || ''}${result.stderr || ''}`
  process.stdout.write(log)
  assert.notEqual(result.status,0,'old page must fail the readback regression')
  assert.match(log,/AssertionError[^]*SAVED_BINDING_READBACK/,'baseline must render and fail the saved-binding assertion, not compile or time out')
  console.log('Confirmed old-page saved-binding display failure; fixed source restored for normal browser test')
} finally {
  await writeFile(path,fixed)
}
