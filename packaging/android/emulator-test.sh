#!/usr/bin/env bash
# Runs the app's phone tests (app/android/app/src/androidTest) on a running emulator or phone:
# installs both APKs, grants the VPN permission the user would grant on the system prompt, and
# fails unless every test passed (am instrument itself always exits 0). Then starts the release
# APK that CI publishes.
# Usage: packaging/android/emulator-test.sh
set -euo pipefail
apks=app/android/app/build/outputs/apk
adb install -r "$apks/website/debug/app-website-debug.apk"
adb install -r "$apks/androidTest/website/debug/app-website-debug-androidTest.apk"
adb shell appops set de.davidvinu.univpn ACTIVATE_VPN allow
result=$(adb shell am instrument -w de.davidvinu.univpn.test/androidx.test.runner.AndroidJUnitRunner)
echo "$result"
adb logcat -d -s uni-vpn:* AndroidRuntime:E | tail -n 50 || true
adb shell run-as de.davidvinu.univpn cat files/state/uni-vpn/daemon.log | tail -n 60 || true
echo "$result" | grep -q '^OK (' || exit 1

# The APK that goes out (minified by R8, signed with the upload key) is the one phones on
# Android 12 and later put in without a tap (ApkUpdate.installUnattended): a release that does
# not start could not update itself out of that. It must start, and its core must come up.
# Without the upload key (a fork) there is only an unsigned one, which Android does not install.
release=out/uni-vpn.apk
[ -f "$release" ] || { echo "no signed release APK: not started"; exit 0; }
adb uninstall de.davidvinu.univpn.test > /dev/null || true
adb uninstall de.davidvinu.univpn > /dev/null
adb install "$release"
adb logcat -c
adb shell am start -W -n de.davidvinu.univpn/.MainActivity
up=""
for _ in $(seq 30); do
  sleep 2
  if adb shell pidof de.davidvinu.univpn > /dev/null && adb shell pidof libunivpn.so > /dev/null; then
    up=yes
    break
  fi
done
# A core that ends at once and is started again would show a new process id each time.
core=$(adb shell pidof libunivpn.so || true)
sleep 5  # a crash a moment after the start
crash=$(adb logcat -d -s AndroidRuntime:E | grep -A 20 'FATAL EXCEPTION' || true)
if [ -z "$up" ] || [ -n "$crash" ] || ! adb shell pidof de.davidvinu.univpn > /dev/null ||
  [ "$(adb shell pidof libunivpn.so || true)" != "$core" ]; then
  echo "the release APK did not start, or its core did not come up" >&2
  echo "$crash" >&2
  adb logcat -d -s uni-vpn:* AndroidRuntime:E | tail -n 50 >&2 || true
  exit 1
fi
echo "release APK started, core running"
