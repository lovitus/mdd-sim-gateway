// Temporary hosted-only acceptance of the published CLI, removed after proof.
const fs=require('node:fs'),cp=require('node:child_process'),path=require('node:path');
const https=require('node:https'),crypto=require('node:crypto'),assert=require('node:assert/strict');
async function main(){
process.umask(0o077);
assert.equal(process.env.GITHUB_ACTIONS,'true');
assert.equal(process.env.RUNNER_OS,'Linux');
const root=fs.mkdtempSync(path.join(process.env.RUNNER_TEMP,'mdd-first-run-'));
const release=path.resolve(process.argv[2]);
const core=path.join(release,'mdd-core'),agent=path.join(release,'mdd-agent');
const run=(args,input)=>cp.execFileSync(args[0],args.slice(1),{input,encoding:'utf8',stdio:['pipe','pipe','pipe']});
const pause=ms=>new Promise(r=>setTimeout(r,ms));
for(const p of ['/etc/mdd','/var/lib/mdd-agent/config.json','/etc/mdd-agent/config.json','/run/pcscd/pcscd.comm'])assert.ok(!fs.existsSync(p),`Pre-existing environment: ${p}`);
assert.equal(fs.readdirSync('/sys/bus/usb/devices').filter(x=>/^\d+-\d/.test(x)).length,0,'USB devices visible');
const home=path.join(root,'home');fs.mkdirSync(home,{mode:0o700});
const password=crypto.randomBytes(32).toString('hex');
fs.writeFileSync(path.join(root,'password'),password);
run(['sudo','useradd','--system','--no-create-home','--shell','/usr/sbin/nologin','mdd']);
const boot=JSON.parse(run(['sudo',core,'bootstrap-host','-public-listen','127.0.0.1:18443','-username','smoke'],password+'\n'));
const ca=run(['sudo','cat','/etc/mdd/tls/server.crt']);
const original=JSON.parse(run(['sudo','cat','/etc/mdd/core.json']));
// Only this ephemeral Core's optional executors are disabled; no installed service.
original.provider_apply={};
fs.writeFileSync(path.join(root,'core.json'),JSON.stringify(original));
fs.writeFileSync(path.join(root,'pin'),boot.tls_certificate_sha256);
const log=fs.openSync(path.join(root,'core.log'),'w',0o600);
let token='',csrf='',child,coreProcess;
function spawn(command,args,options){
  const process=cp.spawn(command,args,options);
  process.completion=new Promise(resolve=>{
    process.once('error',()=>resolve({error:'spawn failed'}));
    process.once('exit',(code,signal)=>resolve({code,signal}));
  });
  process.stdin?.on('error',()=>{});
  return process;
}
async function stopOwned(process,isAgent){
  if(process.exitCode===null&&process.signalCode===null){
    if(isAgent&&!process.stdin.destroyed)process.stdin.write('stop\n');
    else process.kill('SIGINT');
  }
  let timer;
  const result=await Promise.race([process.completion,new Promise(resolve=>{
    timer=setTimeout(()=>{process.kill('SIGKILL');resolve({error:'cleanup timeout'});},15000);
  })]);
  clearTimeout(timer);return result;
}
async function request(route,body){
  const data=body===undefined?undefined:JSON.stringify(body);
  return new Promise((resolve,reject)=>{
    const req=https.request({hostname:'127.0.0.1',port:18443,path:route,ca,method:data?'POST':'GET',
      headers:{Origin:'https://127.0.0.1:18443','Content-Type':'application/json',
        ...(token?{'X-MDD-Session':token,'X-MDD-CSRF-Token':csrf}: {})}},res=>{
      let bytes='';res.on('data',chunk=>bytes+=chunk);res.on('end',()=>resolve({status:res.statusCode,data:JSON.parse(bytes)}));
    });
    req.setTimeout(15000,()=>req.destroy(new Error('Test Core request timeout')));
    req.on('error',reject);req.end(data);
  });
}
const expect=path.join(root,'journey.exp');
fs.writeFileSync(expect,`log_user 0
set timeout 100
set root [lindex $argv 0]
set binary [lindex $argv 1]
set mode [lindex $argv 2]
proc need {pattern} {
  global spawn_id
  expect {
    -re $pattern {puts stderr "CLI prompt matched"}
    timeout {puts stderr "CLI prompt timeout"; exit 1}
    eof {puts stderr "CLI exited before prompt"; exit 1}
  }
}
spawn -noecho $binary run
if {$mode eq "first"} {
  need {Continue this setup.*: }; send -- "yes\\r"
  need {Existing Agent token.*: }; send -- "1\\r"
  need {Core HTTPS URL.*: }; send -- "https://127.0.0.1:18443\\r"
  set f [open "$root/pin" r]; set pin [read $f]; close $f
  need {Trusted leaf SHA-256.*: }; send -- "$pin\\r"
  need {Web username.*: }; send -- "smoke\\r"
  set f [open "$root/password" r]; set secret [read $f]; close $f
  need {Web password.*: }; send -- "$secret\\r"; set secret ""
  need {Starting Agent using the saved configuration.}
}
puts "CLI_READY"; flush stdout
expect_user {
  -re {stop\\n} {}
  timeout {send -- "\\003"; exit 1}
}
send -- "\\003"
expect {
  eof {}
  timeout {puts stderr "CLI did not stop normally"; exit 1}
}
set result [wait]
exit [lindex $result 3]
`);
const env={...process.env,HOME:home,XDG_CONFIG_HOME:path.join(home,'.config'),ADB_MDNS_AUTO_CONNECT:'0'};
delete env.MDD_AGENT_CONFIG;delete env.INVOCATION_ID;
async function launch(mode){
  child=spawn('expect',[expect,root,agent,mode],{env,stdio:['pipe','pipe','pipe']});
  let output='';child.stdout.on('data',b=>output+=b);child.stderr.on('data',b=>process.stderr.write(b));
  const deadline=Date.now()+180000;
  for(const wait of [30000,60000,90000]){
    await pause(Math.min(wait,deadline-Date.now()));
    assert.ok(child.exitCode===null&&child.signalCode===null,'CLI exited unexpectedly');
    if(output.includes('CLI_READY'))return;
  }
  throw Error('CLI setup did not complete');
}
async function stop(){
  const result=await stopOwned(child,true);child=null;
  assert.equal(result.code,0,'CLI graceful exit');
}
try{
  coreProcess=spawn('sudo',[core,'-config',path.join(root,'core.json')],{stdio:['ignore',log,log]});
  await pause(30000);
  const login=await request('/api/auth/login',{username:'smoke',password});assert.equal(login.status,200);
  token=login.data.token;csrf=login.data.csrf;
  await launch('first');
  const configPath=path.join(home,'.config/mdd-agent/config.json');
  const firstBytes=fs.readFileSync(configPath),config=JSON.parse(firstBytes);
  assert.equal(fs.statSync(configPath).mode&0o777,0o600);
  assert.ok(fs.existsSync(path.join(path.dirname(configPath),'state')));
  assert.ok(!fs.existsSync(configPath+'.setup-pending'));
  assert.ok(!firstBytes.includes(password));assert.ok(!config.agent.modem_enabled&&!config.agent.raw_usb_source_enabled&&!config.agent.raw_usb_importer_enabled);
  assert.equal(config.agent.tls_sha256,boot.tls_certificate_sha256);
  const first=(await request('/v1/agents')).data.agents;
  assert.equal(first.length,1);assert.equal(first[0].agent_id,config.agent.id);
  assert.ok(first[0].topology);
  assert.equal((first[0].topology.readers||[]).length,0);assert.equal((first[0].topology.modems||[]).length,0);
  await stop();await launch('reuse');
  assert.deepEqual(fs.readFileSync(configPath),firstBytes,'Default discovery must preserve config/identity/token');
  const second=(await request('/v1/agents')).data.agents;
  assert.equal(second.length,1);assert.equal(second[0].agent_id,config.agent.id);
  assert.notEqual(second[0].process_generation,first[0].process_generation);
  await stop();
  const revoke=await request('/api/auth/agent-credentials',{action:'revoke',agent_id:config.agent.id});
  assert.equal(revoke.status,200);
  await request('/api/auth/logout',{});token='';csrf='';
  console.log(JSON.stringify({publicRelease:'v2.0.0',platform:'linux-amd64',firstRun:true,webLogin:true,
    defaultPath:true,mode:'0600',stateBesideConfig:true,noAdminPasswordSaved:true,pinnedTLS:true,
    connected:true,hardwareCount:0,normalExit:true,reusedIdentity:true,testCredentialRevoked:true,
    productionContacted:false,manualOtherPlatforms:'unverified'}));
}finally{
  for(const process of [child,coreProcess]){
    if(!process)continue;
    const result=await stopOwned(process,process===child);
    if(result.error)console.error('Owned fixture process cleanup failed:',result.error);
  }
  fs.closeSync(log);
}
}
main().catch(error=>{console.error(error);process.exitCode=1;});
