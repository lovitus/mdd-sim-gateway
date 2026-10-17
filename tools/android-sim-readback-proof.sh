#!/usr/bin/env bash
# Temporary hosted negative proof; archived and removed before final delivery.
set -euo pipefail
source=android-agent/app/src/main/java/com/lovitus/mddagent/AgentService.java
cp "$source" "$RUNNER_TEMP/AgentService.fixed.java"
hub=android-agent/app/src/main/java/com/lovitus/mddagent/ReaderHub.java
cp "$hub" "$RUNNER_TEMP/ReaderHub.fixed.java"
trap 'cp "$RUNNER_TEMP/AgentService.fixed.java" "$source"; cp "$RUNNER_TEMP/ReaderHub.fixed.java" "$hub"' EXIT
git show 6b3fddcd0fb7c3a79709470a9b2ffb888d9d7b23:"$source" > "$source"
set +e
gradle -p android-agent --no-daemon connectedDebugAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.class=com.lovitus.mddagent.ReaderReadbackTest#serviceReadbackUsesTypedWireAndRefreshesExistingTopology > proof/service-red.log 2>&1
rc=$?
set -e
test "$rc" -ne 0
cp -R android-agent/app/build/outputs/androidTest-results proof/service-red
grep -Rq 'ANDROID_READBACK_WIRE: no reader_readback_response' proof/service-red
cp "$RUNNER_TEMP/AgentService.fixed.java" "$source"
python3 - <<'PY'
from pathlib import Path
base = Path('android-agent/app/src/main/java/com/lovitus/mddagent')
p = base / 'ReaderHub.java'
s = p.read_text()
s = s.replace('private void invalidate(Entry e){if(e.valid){e.valid=false;topologyRevision++;}}', 'private void invalidate(Entry e){}')
s = s.replace('boolean identityLost(){return entry!=null&&(!entry.valid||entry.insertionSource.get()!=entry.insertion);}', 'boolean identityLost(){return false;}')
s = s.replace('boolean current(){return !closed&&owner.getAsBoolean()&&revision==topologyRevision&&!identityLost()&&android.os.SystemClock.elapsedRealtime()<deadline;}', 'boolean current(){return true;}')
s = s.replace('if(!q.optString("scope").equals("sim_identity"))return result.put("error_code","reader_readback_scope_unsupported");', '')
start = s.index('RunnableCheck check=()->{')
end = s.index('SimProtocol.Card guarded=', start)
s = s[:start] + 'RunnableCheck check=()->{};\n            ' + s[end:]
p.write_text(s)
p = base / 'AgentService.java'
s = p.read_text()
start = s.index('java.util.function.BooleanSupplier current=')
end = s.index('readerIO.execute', start)
s = s[:start] + 'java.util.function.BooleanSupplier current=()->true;\n                ' + s[end:]
p.write_text(s)
PY
set +e
gradle -p android-agent --no-daemon connectedDebugAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.class=com.lovitus.mddagent.ReaderReadbackTest > proof/guards-red.log 2>&1
rc=$?
set -e
test "$rc" -ne 0
cp -R android-agent/app/build/outputs/androidTest-results proof/guards-red
python3 - <<'PY'
from pathlib import Path
from xml.etree import ElementTree as ET
failed = {case.attrib['name'] for p in Path('proof/guards-red').rglob('*.xml') for case in ET.parse(p).iter('testcase') if case.find('failure') is not None}
assert {'rejectsNonSIMScopeAndStaleIdentitiesBeforeAnyCardIO', 'interruptedAndReplacedReadbacksNeverCommitCachedFacts', 'queuedServiceWorkAfterSharingStopsDoesNoIOOrPublication', 'completedReadMustStillOwnItsIdentityAndDeadlineAtPublication', 'replacementBeforeSocketSendWithdrawsOnlyTheOriginalCard'} <= failed, failed
PY
cp "$RUNNER_TEMP/AgentService.fixed.java" "$source"
cp "$RUNNER_TEMP/ReaderHub.fixed.java" "$hub"
gradle -p android-agent --no-daemon connectedDebugAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.class=com.lovitus.mddagent.ReaderReadbackTest,com.lovitus.mddagent.LinkTest
cp -R android-agent/app/build/outputs/androidTest-results proof/service-green
