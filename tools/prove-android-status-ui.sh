#!/usr/bin/env bash
# Temporary candidate-only proof; archive its receipt and remove before delivery.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
mode=${1:?red or green required}
baseline=1bfcf9a3539939fc12f0ed4737871b0841e8f6c9
source=android-agent/app/src/main/java/com/lovitus/mddagent/MainActivity.java
output=android-ui-proof
results=android-agent/app/build/outputs/androidTest-results/connected
mkdir -p "$output/$mode"
if [[ "$mode" == red ]]; then
  git cat-file -e "$baseline^{commit}" 2>/dev/null || git fetch --no-tags --depth=1 origin "$baseline"
  cp "$source" "$output/MainActivity.candidate.java"
  trap 'cp "$output/MainActivity.candidate.java" "$source"' EXIT
  git show "$baseline:$source" > "$source"
  git rev-parse HEAD HEAD^{tree} > "$output/source.txt"
  printf 'baseline=%s\n' "$baseline" >> "$output/source.txt"
  sha256sum "$source" "$output/MainActivity.candidate.java" >> "$output/source.txt"
  cases=com.lovitus.mddagent.CommunicationUxTest#homeSeparatesPartialObservationAndClearsStalePresentation
  cases+=,com.lovitus.mddagent.CommunicationUxTest#longReplyDirectoryShowsProvenCardWithoutChangingUserChoice
  cases+=,com.lovitus.mddagent.LoginFlowTest#draftFeedbackDistinguishesSavedPrivateAndFailedWithoutReplacingInputs
  set +e
  gradle -p android-agent --no-daemon connectedDebugAndroidTest "-Pandroid.testInstrumentationRunnerArguments.class=$cases" > "$output/red/gradle.log" 2>&1
  result=$?
  set -e
  [[ $result != 0 ]] || { echo 'Old source unexpectedly passed'; exit 1; }
elif [[ "$mode" != green ]]; then
  echo 'Expected red or green'; exit 1
fi
cp -R "$results" "$output/$mode/results"
extra=android-agent/app/build/outputs/connected_android_test_additional_output
if [[ -d "$extra" ]]; then cp -R "$extra" "$output/$mode/screens"; fi
python3 - "$mode" "$output/$mode" <<'PY'
import json
import pathlib
import sys
import xml.etree.ElementTree as ET

mode, folder = sys.argv[1], pathlib.Path(sys.argv[2])
expected = {
    ('com.lovitus.mddagent.CommunicationUxTest', 'homeSeparatesPartialObservationAndClearsStalePresentation'): '[UX-HOME-PARTIAL]',
    ('com.lovitus.mddagent.CommunicationUxTest', 'longReplyDirectoryShowsProvenCardWithoutChangingUserChoice'): '[UX-REPLY-VISIBLE]',
    ('com.lovitus.mddagent.LoginFlowTest', 'draftFeedbackDistinguishesSavedPrivateAndFailedWithoutReplacingInputs'): '[UX-DRAFT-SAVED]',
}
seen = set()
total = 0
for path in sorted((folder / 'results').rglob('TEST-*.xml')):
    root = ET.parse(path).getroot()
    for case in root.iter('testcase'):
        total += 1
        key = (case.get('classname'), case.get('name'))
        assert case.find('error') is None and case.find('skipped') is None, (key, 'error/skip')
        failures = case.findall('failure')
        if mode == 'red':
            assert key in expected and len(failures) == 1, (key, 'unexpected outcome')
            text = failures[0].get('message', '') + ''.join(failures[0].itertext())
            assert expected[key] in text, (key, 'not the intended behavioral failure', text)
        else:
            assert not failures, (key, 'fixed regression failed')
        if key in expected:
            assert key not in seen, (key, 'duplicate result')
            seen.add(key)
assert seen == set(expected), ('missing executed regression', seen)
assert total == 3 if mode == 'red' else total >= 3
receipt = dict(mode=mode, total=total, targetTests=sorted('#'.join(k) for k in seen), errors=0, skipped=0,
               expectedBehavioralFailures=3 if mode == 'red' else 0)
(folder / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
print(json.dumps(receipt))
PY
if [[ "$mode" == red ]]; then
  cp "$output/MainActivity.candidate.java" "$source"
  cmp "$source" "$output/MainActivity.candidate.java"
  trap - EXIT
fi
