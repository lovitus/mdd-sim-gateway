#!/usr/bin/env bash
set -euo pipefail
port=${1:?explicit isolated CI ADB port required}
serial=${2:?explicit emulator serial required}
suite=${3:-process}
[[ "$port" =~ ^[0-9]+$ && "$serial" == emulator-* && ( "$suite" == process || "$suite" == control ) ]] || exit 2
adb_bin=$(command -v adb)
device_adb() { "$adb_bin" -P "$port" -s "$serial" "$@"; }
# This workflow owns a disposable VM with exactly this emulator. Gradle's
# per-process environment is pinned only after verifying the server inventory.
connected=$("$adb_bin" -P "$port" devices | awk '$2 == "device" { print $1 }')
[[ "$connected" == "$serial" ]] || { printf '%s\n' 'Unexpected CI ADB inventory; refusing test dispatch' >&2; exit 2; }
device_adb get-state

collect_screens() {
  mkdir -p android-screens
  while IFS= read -r directory; do
    [[ "$directory" == /data/local/tmp/mdd-native-ui-* ]] || continue
    device_adb pull "$directory" android-screens/ >/dev/null || true
  done < <(device_adb shell 'ls -d /data/local/tmp/mdd-native-ui-*' 2>/dev/null | tr -d '\r')
}
trap collect_screens EXIT

if [[ "$suite" == process ]]; then
  ANDROID_ADB_SERVER_PORT="$port" ANDROID_SERIAL="$serial" gradle -p android-agent --no-daemon connectedDebugAndroidTest
  collect_screens
  for page in home calls messages readers settings; do
    test -n "$(find android-screens -name "$page.png" -size +0c -print -quit)"
  done
fi
# Gradle's disposable debug signature differs from the Build job's QA APK.
# Only this VM's test packages may be replaced; normal preview is never cleared.
for package in com.lovitus.mddagent.preview.qa.test com.lovitus.mddagent.preview.qa; do
  if [[ -n "$(device_adb shell pm list packages "$package" | tr -d '\r' | awk -v expected="package:$package" '$0 == expected')" ]]; then
    device_adb uninstall "$package"
  fi
done
qa_apk=release-bundle/android-agent/app/build/outputs/apk/debug/app-debug.apk
device_adb install "$qa_apk"
(cd process-fixture && sha256sum --check SHA256SUMS)
chmod 700 process-fixture/fixture-linux-amd64
evidence="android-$suite-evidence"
mkdir -m 700 "$evidence"
for mode in cellular vowifi; do
  node android-agent/scripts/verify-process-recovery.mjs \
    --adb "$adb_bin" --port "$port" --serial "$serial" --mode "$mode" --suite "$suite" \
    --fixture "$PWD/process-fixture/fixture-linux-amd64" \
    --fixture-sha256 "$(sha256sum process-fixture/fixture-linux-amd64 | cut -d' ' -f1)" \
    --apk-sha256 "$(sha256sum "$qa_apk" | cut -d' ' -f1)" \
    --output "$PWD/$evidence/$mode"
done
if [[ "$suite" != process ]]; then exit 0; fi
if [[ "${MDD_SKIP_PREVIEW_INSTALL:-false}" == true ]]; then exit 0; fi
device_adb install -r release-bundle/android-evidence/mdd-agent-preview.apk
device_adb shell am start -W -n com.lovitus.mddagent.preview/com.lovitus.mddagent.MainActivity
device_adb shell uiautomator dump /data/local/tmp/mdd-preview-layout.xml
device_adb pull /data/local/tmp/mdd-preview-layout.xml android-screens/preview-layout.xml
device_adb exec-out screencap -p > android-screens/setup.png
