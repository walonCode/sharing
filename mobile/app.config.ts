import type { ConfigContext, ExpoConfig } from "expo/config";

/**
 * app.json holds the static config. This adds what depends on the build:
 *  - no backups of app data (the device token and transfer queue must not leave the phone);
 *  - the permissions behind the optional automatic phone-to-laptop clipboard
 *    (READ_LOGS, SYSTEM_ALERT_WINDOW) are declared only when SHARING_AUTO_CLIPBOARD=1
 *    (`make apk-auto-clipboard`); the default build does not request them at all.
 *
 * The Expo template also adds storage, vibrate and overlay permissions that this app does not use
 * (it writes through MediaStore and reads picked content), so they are blocked explicitly.
 */
export default ({ config }: ConfigContext): ExpoConfig => {
  const autoClipboard = process.env.SHARING_AUTO_CLIPBOARD === "1";
  const clipboardPermissions = ["android.permission.READ_LOGS", "android.permission.SYSTEM_ALERT_WINDOW"];
  const unused = ["android.permission.READ_EXTERNAL_STORAGE", "android.permission.WRITE_EXTERNAL_STORAGE", "android.permission.VIBRATE"];
  return {
    ...config,
    name: config.name ?? "Sharing",
    slug: config.slug ?? "sharing",
    android: {
      ...config.android,
      allowBackup: false,
      permissions: autoClipboard ? clipboardPermissions : [],
      blockedPermissions: autoClipboard ? unused : [...unused, ...clipboardPermissions],
    },
  };
};
