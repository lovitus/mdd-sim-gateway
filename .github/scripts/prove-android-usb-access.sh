#!/usr/bin/env bash
set -euo pipefail
source_dir=android-agent/app/src/main/java/com/lovitus/mddagent
cp "$source_dir/AgentService.java" "$RUNNER_TEMP/usb-access-AgentService.java"
cp "$source_dir/ReaderUsbAccess.java" "$RUNNER_TEMP/usb-access-ReaderUsbAccess.java"
restore() {
  cp "$RUNNER_TEMP/usb-access-AgentService.java" "$source_dir/AgentService.java"
  cp "$RUNNER_TEMP/usb-access-ReaderUsbAccess.java" "$source_dir/ReaderUsbAccess.java"
}
trap restore EXIT
mkdir -p usb-access-proof
git rev-parse HEAD HEAD^{tree} > usb-access-proof/source.txt
python3 - <<'PY'
from pathlib import Path
base = Path('android-agent/app/src/main/java/com/lovitus/mddagent')
def replace(file, old, new):
    p = base / file
    source = p.read_text()
    assert source.count(old) == 1
    p.write_text(source.replace(old, new))
# Restore the three reviewed behavioral defects without removing any compiled API.
replace('ReaderUsbAccess.java', 'private static final Set<String> attempted', 'private final Set<String> attempted')
replace('ReaderUsbAccess.java', 'pendingNonce.isEmpty() || pendingAttachment.equals(attachment);',
        'pendingNonce.isEmpty() || pendingAttachment.equals(attachment)&&expired();')
replace('AgentService.java', 'if((getPackageName()+".USB_PERMISSION").equals(i.getAction())){',
        'UsbDevice unchecked=i.getParcelableExtra(UsbManager.EXTRA_DEVICE);\n        if((getPackageName()+".USB_PERMISSION").equals(i.getAction())){')
PY
suite=com.lovitus.mddagent.ReaderUsbAccessFlowTest
set +e
gradle -p android-agent --no-daemon connectedDebugAndroidTest \
  "-Pandroid.testInstrumentationRunnerArguments.class=$suite#serviceRecreationPreservesRefusalForStillAttachedReader,$suite#pausedManualRetryRemainsReachableWithoutATick,$suite#invalidPermissionInputCannotCrashTheRegisteredReceiver" \
  > usb-access-proof/red.log 2>&1
red=$?
set -e
test "$red" -ne 0
cp -R android-agent/app/build/outputs/androidTest-results usb-access-proof/red-results
python3 - <<'PY'
from pathlib import Path
import xml.etree.ElementTree as ET
expected = {
  'serviceRecreationPreservesRefusalForStillAttachedReader': 'USB_ACCESS_BUDGET',
  'pausedManualRetryRemainsReachableWithoutATick': 'USB_ACCESS_RETRY',
  'invalidPermissionInputCannotCrashTheRegisteredReceiver': 'USB_PERMISSION_INPUT',
}
cases = [c for p in Path('usb-access-proof/red-results').rglob('TEST-*.xml')
         for c in ET.parse(p).iter('testcase')]
assert len(cases) == 3
for c in cases:
    failure = c.find('failure')
    assert failure is not None and c.find('error') is None and c.find('skipped') is None, c.get('name')
    assert expected[c.get('name')] in ''.join(failure.itertext()) + failure.get('message', ''), ET.tostring(c)
print('Three review counterexamples failed behaviorally.')
PY
restore
gradle -p android-agent --no-daemon connectedDebugAndroidTest \
  "-Pandroid.testInstrumentationRunnerArguments.class=$suite" > usb-access-proof/green.log 2>&1
cp -R android-agent/app/build/outputs/androidTest-results usb-access-proof/green-results
python3 - <<'PY'
from pathlib import Path
import xml.etree.ElementTree as ET
cases = [c for p in Path('usb-access-proof/green-results').rglob('TEST-*.xml')
         for c in ET.parse(p).iter('testcase')]
assert len(cases) == 8
assert all(c.find('failure') is None and c.find('error') is None and c.find('skipped') is None for c in cases)
print('All eight restored Service/Activity/PendingIntent flows passed.')
PY
git diff --exit-code -- "$source_dir/AgentService.java" "$source_dir/ReaderUsbAccess.java"
