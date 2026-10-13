// Temporary hosted-only proof; archive the candidate, then remove this entry.
import fs from 'node:fs'
import {execFileSync,spawnSync} from 'node:child_process'
const base='9a9aa783a5681ec203cd977b06c9582979dc7331'
const proof='communication-proof.log'
const cases=[
  {name:'sms',files:['webui/src/mdd/views/Messages.jsx'],marker:'SMS_JOURNAL'},
  {name:'call',files:['webui/src/goCallCoordinator.jsx'],marker:'CALL_OWNERSHIP'},
  {name:'allowance',files:['webui/src/mdd/views/AllowancePanel.jsx','webui/src/api.js'],marker:'ALLOWANCE_CONFIRMATION'},
  {name:'core',files:['go-runtime/internal/allowance/service.go','go-runtime/internal/allowance/store.go'],marker:'CONFIRMATION_FENCE'},
]
function run(name,red){
  const result=name==='core'
    ? spawnSync('go',['test','-race','-count=1','-run','^TestConfirmationFence$','./internal/allowance'],{cwd:'go-runtime',encoding:'utf8',maxBuffer:20<<20})
    : spawnSync('node',['tests/communicationBrowser.mjs'],{cwd:'webui',env:{...process.env,MDD_COMMUNICATION_CASE:name},encoding:'utf8',maxBuffer:20<<20})
  const output=String(result.stdout||'')+String(result.stderr||'')
  fs.appendFileSync(proof,`\n${red?'OLD':'FIXED'} ${name}\n${output}`)
  return {...result,output}
}
fs.writeFileSync(proof,`Baseline ${base}; exact legacy bodies with new typed request model retained for compiled confirmation counterexample. No carrier traffic.\n`)
for(const item of cases){
  const fixed=new Map(item.files.map(file=>[file,fs.readFileSync(file)]))
  try {
    for(const file of item.files)fs.writeFileSync(file,execFileSync('git',['show',`${base}:${file}`]))
    const red=run(item.name,true)
    if(red.status===0||!red.output.includes(item.marker)||/build failed|undefined:|SyntaxError/.test(red.output))throw Error(`Invalid old-behavior proof: ${item.name}`)
    console.log(`Old ${item.name}: expected behavioral failure`)
  }finally{for(const [file,bytes]of fixed)fs.writeFileSync(file,bytes)}
}
const core=spawnSync('go',['test','-race','./internal/allowance'],{cwd:'go-runtime',encoding:'utf8',maxBuffer:20<<20})
fs.appendFileSync(proof,`\nFIXED package\n${core.stdout}${core.stderr}`)
if(core.status!==0)throw Error('Fixed allowance package failed')
const green=run('',false)
if(green.status!==0)throw Error('Fixed complete browser flow failed')
console.log('Compiled Core and real browser old reds/fixed green passed')
