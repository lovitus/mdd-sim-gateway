#!/usr/bin/env bash
# Temporary hosted-only reproduction of the reported tiny-dot status ambiguity.
set -euo pipefail
test "${GITHUB_ACTIONS:-}" = true
baseline=38af2d406dd8925750c3bf4d946d8bec0350c422
source_file=android-agent/app/src/main/java/com/lovitus/mddagent/LineStatusView.java
saved="$RUNNER_TEMP/mdd-line-status-LineStatusView.java"
evidence="$PWD/android-line-status-evidence"
test ! -e "$saved"
mkdir -p "$evidence"
git rev-parse HEAD HEAD^{tree} > "$evidence/candidate-source.txt"
git rev-parse "$baseline" "$baseline^{tree}" > "$evidence/baseline-source.txt"
cp "$source_file" "$saved"
restore() { mv "$saved" "$source_file"; }
trap restore EXIT
git show "$baseline:$source_file" > "$source_file"
set +e
gradle -p android-agent --no-daemon connectedDebugAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.class=com.lovitus.mddagent.CommunicationUxTest#lineAvailabilitySummaryAndFullTextFollowCallAndSmsStates \
  > "$evidence/baseline.log" 2>&1
result=$?
set -e
cp -R android-agent/app/build/outputs/androidTest-results "$evidence/baseline-results"
python3 - "$evidence/baseline-results" "$result" <<'PY'
import pathlib, sys, xml.etree.ElementTree as ET
assert int(sys.argv[2]) != 0, 'Baseline unexpectedly passed'
cases = []
for file in pathlib.Path(sys.argv[1]).rglob('*.xml'):
    cases.extend(ET.parse(file).iter('testcase'))
assert len(cases) == 1, 'The selected native regression must execute exactly once'
case = cases[0]
assert case.attrib['classname'] == 'com.lovitus.mddagent.CommunicationUxTest'
assert case.attrib['name'] == 'lineAvailabilitySummaryAndFullTextFollowCallAndSmsStates'
assert case.find('error') is None and case.find('skipped') is None
failure = case.find('failure')
assert failure is not None
assert 'Entire ready status must be colored, not only a tiny dot' in ''.join(failure.itertext()) + str(failure.attrib)
print('Compiled old-view color failure confirmed; restoring candidate for native suite')
PY
restore
trap - EXIT
