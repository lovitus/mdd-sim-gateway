#!/usr/bin/env bash
set -euo pipefail
source_file=android-agent/app/src/main/java/com/lovitus/mddagent/AgentService.java
cp "$source_file" "$RUNNER_TEMP/usb-access-AgentService.java"
trap 'cp "$RUNNER_TEMP/usb-access-AgentService.java" "$source_file"' EXIT
mkdir -p usb-access-proof
git rev-parse HEAD HEAD^{tree} > usb-access-proof/source.txt
python3 - <<'PY'
from pathlib import Path
p = Path('android-agent/app/src/main/java/com/lovitus/mddagent/AgentService.java')
source = p.read_text()
dispatch = 'if(usbAccessCheckPending.compareAndSet(false,true))'
assert source.count(dispatch) == 1
# Withdraw only the fix's automatic dispatch. The real Service and tests still compile.
p.write_text(source.replace(dispatch, 'if(false&&usbAccessCheckPending.compareAndSet(false,true))'))
PY
set +e
gradle -p android-agent --no-daemon connectedDebugAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.class=com.lovitus.mddagent.ReaderUsbAccessFlowTest \
  > usb-access-proof/red.log 2>&1
red=$?
set -e
test "$red" -ne 0
cp -R android-agent/app/build/outputs/androidTest-results usb-access-proof/red-results
python3 - <<'PY'
from pathlib import Path
import xml.etree.ElementTree as ET
cases = []
for p in Path('usb-access-proof/red-results').rglob('TEST-*.xml'):
    cases.extend(c for c in ET.parse(p).iter('testcase')
                 if c.get('classname') == 'com.lovitus.mddagent.ReaderUsbAccessFlowTest')
assert len(cases) == 5, f'Expected five executed counterexamples, got {len(cases)}'
for c in cases:
    failure = c.find('failure')
    assert failure is not None and c.find('error') is None and c.find('skipped') is None, c.get('name')
    assert 'AUTO_USB_ACCESS' in ''.join(failure.itertext()) + failure.get('message', ''), ET.tostring(c)
print('Five behavioral counterexamples failed because automatic OS access was never requested.')
PY
cp "$RUNNER_TEMP/usb-access-AgentService.java" "$source_file"
gradle -p android-agent --no-daemon connectedDebugAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.class=com.lovitus.mddagent.ReaderUsbAccessFlowTest \
  > usb-access-proof/green.log 2>&1
cp -R android-agent/app/build/outputs/androidTest-results usb-access-proof/green-results
python3 - <<'PY'
from pathlib import Path
import xml.etree.ElementTree as ET
cases = []
for p in Path('usb-access-proof/green-results').rglob('TEST-*.xml'):
    cases.extend(c for c in ET.parse(p).iter('testcase')
                 if c.get('classname') == 'com.lovitus.mddagent.ReaderUsbAccessFlowTest')
assert len(cases) == 5
assert all(c.find('failure') is None and c.find('error') is None and c.find('skipped') is None for c in cases)
print('Five restored Service/Activity/PendingIntent flows passed.')
PY
git diff --exit-code -- "$source_file"
