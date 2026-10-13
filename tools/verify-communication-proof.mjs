// Temporary hosted-only continuation of the archived communication proof.
import fs from 'node:fs'
import {execFileSync,spawnSync} from 'node:child_process'
const base='609afc67454c9cfa23e32dda08e78639a59cd418'
const file='webui/src/mdd/views/Messages.jsx', fixed=fs.readFileSync(file)
const log='communication-proof.log'
function run(mode) {
  const result=spawnSync('node',['tests/communicationBrowser.mjs'],{cwd:'webui',env:{...process.env,MDD_COMMUNICATION_CASE:mode},encoding:'utf8',maxBuffer:20<<20})
  const output=String(result.stdout||'')+String(result.stderr||'')
  fs.appendFileSync(log,`\n${mode || 'FIXED complete'}\n${output}`)
  return {...result,output}
}
fs.writeFileSync(log,`Prior qualified archive/2026-10-13/communication-qualified; history baseline ${base}.\n`)
try {
  fs.writeFileSync(file,execFileSync('git',['show',`${base}:${file}`]))
  const red=run('sms-history')
  if(red.status===0||!red.output.includes('SMS_HISTORY')||/SyntaxError|build failed|TimeoutError/.test(red.output))throw Error('Invalid history ownership counterexample')
} finally {fs.writeFileSync(file,fixed)}
if(run('').status!==0)throw Error('Fixed complete communication browser failed')
console.log('Old scoped-history ownership fails; fixed complete browser passes')
