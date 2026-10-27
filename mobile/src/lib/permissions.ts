import { useCallback, useEffect, useState } from "react";
import { AppState, PermissionsAndroid, Platform } from "react-native";
import { SharingService } from "./sharing";

/** Runs [refresh] whenever the app returns to the foreground: permissions change in system settings or over adb. */
export function useRefreshOnForeground(refresh: () => void) {
  useEffect(() => {
    const sub = AppState.addEventListener("change", (state) => {
      if (state === "active") refresh();
    });
    return () => sub.remove();
  }, [refresh]);
}

async function notificationsGranted(): Promise<boolean> {
  if (Platform.OS !== "android" || Platform.Version < 33) return true;
  return PermissionsAndroid.check(PermissionsAndroid.PERMISSIONS.POST_NOTIFICATIONS);
}

/** Notification, battery and quick-settings-tile state for the permissions screen. */
export function usePermissions() {
  const [notificationsOk, setNotificationsOk] = useState(false);
  const [batteryOk, setBatteryOk] = useState(() => SharingService.isBatteryOptimizationIgnored());
  const [tileAdded, setTileAdded] = useState(false);
  const [tileHint, setTileHint] = useState<string | null>(null);

  const refresh = useCallback(() => {
    setBatteryOk(SharingService.isBatteryOptimizationIgnored());
    notificationsGranted().then(setNotificationsOk);
  }, []);

  // The first notification check is async, so state is only set from its callback, never synchronously in the effect.
  useEffect(() => {
    let live = true;
    notificationsGranted().then((ok) => live && setNotificationsOk(ok));
    return () => {
      live = false;
    };
  }, []);
  useRefreshOnForeground(refresh);

  const allowNotifications = useCallback(async () => {
    if (Platform.OS !== "android") return;
    const result = await PermissionsAndroid.request(PermissionsAndroid.PERMISSIONS.POST_NOTIFICATIONS);
    setNotificationsOk(result === PermissionsAndroid.RESULTS.GRANTED || Platform.Version < 33);
  }, []);

  const allowBattery = useCallback(() => {
    SharingService.requestIgnoreBatteryOptimization();
    setTimeout(() => setBatteryOk(SharingService.isBatteryOptimizationIgnored()), 1500);
  }, []);

  const addTile = useCallback(async () => {
    const result = await SharingService.requestAddClipboardTile();
    setTileAdded(result !== "unsupported" && !/not_added|error/i.test(result));
    setTileHint(result === "unsupported" ? "Pull down Quick Settings, tap the pencil, and drag “Send clipboard” in." : null);
  }, []);

  return { notificationsOk, batteryOk, tileAdded, tileHint, allowNotifications, allowBattery, addTile };
}

/**
 * Automatic-clipboard access, read from native once and again only when the app
 * returns to the foreground or [refresh] is called - not on every status event.
 */
export function useClipboardAccess() {
  const [access, setAccess] = useState(() => SharingService.clipboardAccess());
  const refresh = useCallback(() => setAccess(SharingService.clipboardAccess()), []);
  useRefreshOnForeground(refresh);
  return { access, refresh };
}
