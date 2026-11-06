// One-time hosted behavioral counterexamples. Removed after qualification.
import fs from 'node:fs'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
const root = process.cwd()
const records = path.join(process.env.RUNNER_TEMP, 'cellular-coexistence-proof')
fs.mkdirSync(records, { recursive: true })
// Original7 reds: run42153737616. Review-defect7 reds: run42156044160.
// Both are retained as evidence and must not run again.
const green=spawnSync('go',['-C','go-runtime','test','-json','-race','-count=1','-timeout=3m',...['agentdata','agentcall','agentconnection','agentpolicy','agenthost','cellulardata','egressexec','linuxmodem'].map(p=>'./internal/'+p)],{encoding:'utf8',maxBuffer:32*1024*1024})
fs.writeFileSync(path.join(records,'green.jsonl'),green.stdout+green.stderr)
if(green.status!==0) { process.stdout.write(green.stdout); process.stderr.write(green.stderr); process.exit(1) }
console.log('Relevant packages pass with the production fixes restored; full normal CI remains separate.')
