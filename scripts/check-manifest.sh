#!/bin/sh
# Assertions on the merged release manifest of the Android app (run after
# `cd mobile/android && ./gradlew :app:processReleaseMainManifest`).
#   scripts/check-manifest.sh [path/to/AndroidManifest.xml] [--auto-clipboard]
# The default build must not request READ_LOGS or SYSTEM_ALERT_WINDOW at all; the opt-in build must.
set -eu

auto=0
manifest=""
for a in "$@"; do
  case "$a" in
    --auto-clipboard) auto=1 ;;
    *) manifest="$a" ;;
  esac
done
if [ -z "$manifest" ]; then
  manifest=$(find "$(dirname "$0")/../mobile/android/app/build/intermediates" -path '*release*' -name AndroidManifest.xml -path '*merged_manifest*' 2>/dev/null | head -1)
fi
[ -n "$manifest" ] && [ -f "$manifest" ] || { echo "no merged manifest found; build first" >&2; exit 2; }
echo "checking $manifest"

fail=0
check() { # description, command
  if eval "$2" >/dev/null 2>&1; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi
}
has() { grep -q "$1" "$manifest"; }

check 'allowBackup is false' "has 'android:allowBackup=\"false\"'"
if [ "$auto" = 1 ]; then
  check 'READ_LOGS declared in the opt-in build' "has 'android.permission.READ_LOGS'"
  check 'SYSTEM_ALERT_WINDOW declared in the opt-in build' "has 'android.permission.SYSTEM_ALERT_WINDOW'"
else
  check 'READ_LOGS not requested' "! has 'uses-permission[^>]*android.permission.READ_LOGS'"
  check 'SYSTEM_ALERT_WINDOW not requested' "! has 'uses-permission[^>]*android.permission.SYSTEM_ALERT_WINDOW'"
fi
for p in WAKE_LOCK ACCESS_WIFI_STATE CHANGE_WIFI_STATE READ_EXTERNAL_STORAGE WRITE_EXTERNAL_STORAGE VIBRATE; do
  check "$p not requested" "! has 'uses-permission[^>]*android.permission.$p\"'"
done

# exported flags: BootReceiver must not be exported; the share target and tile are the only exported components of the module.
receiver=$(tr '\n' ' ' < "$manifest" | grep -o '<receiver[^>]*BootReceiver[^>]*>' || true)
check 'BootReceiver present' "[ -n \"$receiver\" ]"
check 'BootReceiver not exported' "echo '$receiver' | grep -q 'android:exported=\"false\"'"
for c in SharingService ClipboardSendActivity; do
  comp=$(tr '\n' ' ' < "$manifest" | grep -o "<[a-z]*[^>]*sharingservice.$c[^>]*>" || true)
  check "$c not exported" "echo '$comp' | grep -q 'android:exported=\"false\"'"
done
exit $fail
