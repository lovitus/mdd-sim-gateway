from pathlib import Path
import subprocess
r=Path('candidate/android-agent/app/src')
p=r/'main/java/com/lovitus/mddagent/MainActivity.java'
s=p.read_text().replace('setId(1001)','setId(R.id.connect_gateway)').replace('findViewById(1001)','findViewById(R.id.connect_gateway)')
s=s.replace('if(service!=null&&service.call!=null){error("Resolve the current call first");return;}', 'if(service!=null&&service.accountBusy()){error("Resolve the current call or wait for the current operation first");return;}')
p.write_text(s)
p=r/'androidTest/java/com/lovitus/mddagent/DeviceTest.java';p.write_text(p.read_text().replace('findViewById(1001)','findViewById(R.id.connect_gateway)'))
p=r/'main/res/values/ids.xml';p.write_text('<resources><item type="id" name="connect_gateway" /></resources>\n')
p=r/'main/AndroidManifest.xml';p.write_text(p.read_text().replace('android:fullBackupContent="false"','android:fullBackupContent="false" android:dataExtractionRules="@xml/no_backup"'))
p=r/'main/res/xml/no_backup.xml';p.parent.mkdir(exist_ok=True);p.write_text('''<?xml version="1.0" encoding="utf-8"?>
<data-extraction-rules>
    <cloud-backup disableIfNoEncryptionCapabilities="true">
        <exclude domain="sharedpref" path="." />
        <exclude domain="database" path="." />
        <exclude domain="file" path="." />
        <exclude domain="root" path="." />
        <exclude domain="external" path="." />
    </cloud-backup>
    <device-transfer>
        <exclude domain="sharedpref" path="." />
        <exclude domain="database" path="." />
        <exclude domain="file" path="." />
        <exclude domain="root" path="." />
        <exclude domain="external" path="." />
    </device-transfer>
</data-extraction-rules>
''')
p=r/'main/java/com/lovitus/mddagent/AgentService.java';s=p.read_text()
s=s.replace('private final AtomicBoolean smsPending=new AtomicBoolean();','private final AtomicBoolean smsPending=new AtomicBoolean(),enrollmentPending=new AtomicBoolean();')
s=s.replace('    private void startReaderLink(){','    private void startReaderLink(){\n        if(agent!=null){agent.close();agent=null;}')
s=s.replace('        notice="Enrolling reader access…";', '        if(!enrollmentPending.compareAndSet(false,true)){notice="Reader enrollment already in progress";changed();return;}\n        notice="Enrolling reader access…";')
s=s.replace('main.post(()->{if(!available||api!=owner)return;save("agent_id",identity);','main.post(()->{try{if(!available||api!=owner)return;save("agent_id",identity);')
s=s.replace('notice="Reader sharing enabled. Grant USB access if prompted.";changed();});','notice="Reader sharing enabled. Grant USB access if prompted.";changed();}finally{enrollmentPending.set(false);}});')
s=s.replace('notice="Reader enrollment failed: "+RemoteCall.safe(e);changed();}});','notice="Reader enrollment failed: "+RemoteCall.safe(e);enrollmentPending.set(false);changed();}});')
s=s.replace('    boolean sharing(){return sharing;}','    boolean sharing(){return sharing;}\n    boolean accountBusy(){return call!=null||smsPending.get()||enrollmentPending.get();}')
s=s.replace('void pause(){if(call!=null)','void pause(){if(smsPending.get()){notice="Wait for the current message result before pausing";changed();return;}if(call!=null)')
s=s.replace('void logout(){if(call!=null)','void logout(){if(smsPending.get()){notice="Wait for the current message result before signing out";changed();return;}if(call!=null)')
p.write_text(s)
p=r/'test/java/com/lovitus/mddagent/GatewayApiTest.java';p.write_text(p.read_text().replace('            try{\n', '            try{\n                assertEquals(30000,api.http.pingIntervalMillis());assertFalse(api.http.retryOnConnectionFailure());\n',1))
p=r/'androidTest/java/com/lovitus/mddagent/LinkTest.java';p.write_bytes(Path('harness/audit/android/LinkTest.java').read_bytes())
subprocess.run(['git','-C','candidate','add','android-agent'],check=True)
assert subprocess.check_output(['git','-C','candidate','write-tree'],text=True).strip()=='f43894ff49820b3066c9b6bef79858d699d0e57f'
