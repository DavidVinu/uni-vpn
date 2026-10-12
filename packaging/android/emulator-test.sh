#!/usr/bin/env bash
# Runs the app's phone tests (app/android/app/src/androidTest) on a running emulator or phone:
# installs both APKs, grants the VPN permission the user would grant on the system prompt, and
# fails unless every test passed (am instrument itself always exits 0).
# Usage: packaging/android/emulator-test.sh
set -euo pipefail
apks=app/android/app/build/outputs/apk
adb install -r "$apks/debug/app-debug.apk"
adb install -r "$apks/androidTest/debug/app-debug-androidTest.apk"
adb shell appops set de.davidvinu.univpn ACTIVATE_VPN allow
result=$(adb shell am instrument -w de.davidvinu.univpn.test/androidx.test.runner.AndroidJUnitRunner)
echo "$result"
adb logcat -d -s uni-vpn:* AndroidRuntime:E | tail -n 50 || true
adb shell run-as de.davidvinu.univpn cat files/state/uni-vpn/daemon.log | tail -n 60 || true
echo "$result" | grep -q '^OK ('
