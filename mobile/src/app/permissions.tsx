import { router } from "expo-router";
import { ScrollView, StyleSheet, Switch, Text, View } from "react-native";
import { Button } from "../components/Button";
import { Card, Step } from "../components/Step";
import { useClipboardAccess, usePermissions } from "../lib/permissions";
import { SharingService, useSharingStatus } from "../lib/sharing";
import { colors, spacing } from "../theme";

/** Background-running permissions and the clipboard options. */
export default function Permissions() {
  const status = useSharingStatus();
  const p = usePermissions();
  const { access, refresh } = useClipboardAccess();

  return (
    <ScrollView contentContainerStyle={styles.container}>
      <Step n={1} title="Allow background running" done={p.notificationsOk && p.batteryOk} doneText="Allowed">
        <Button label="Allow notifications" onPress={p.allowNotifications} />
        <Button label="Don't optimise battery" onPress={p.allowBattery} />
      </Step>

      <Step n={2} title="Copy from phone to laptop" done={p.tileAdded} doneText="Tile added">
        <Text style={styles.hint}>
          Android stops apps reading the clipboard in the background, so phone to laptop copying takes one tap: copy as usual, then tap
          “Send clipboard” on the tile or in the notification. Laptop to phone is automatic.
        </Text>
        <Button label="Add “Send clipboard” tile" onPress={p.addTile} />
        {p.tileHint ? <Text style={styles.hint}>{p.tileHint}</Text> : null}
      </Step>

      <Card title="Automatic copy from phone (optional, advanced)">
        {access.supported ? (
          <>
            <Text style={styles.hint}>
              Skips the tap. It needs two extra permissions and works around an Android privacy rule, so it is off by default. It may show
              Android&apos;s &quot;pasted from clipboard&quot; notice and can stop working after a system update.
            </Text>
            <Text style={styles.hint}>{access.overlay ? "✓" : "✗"} Display over other apps</Text>
            {!access.overlay ? (
              <Button label="Allow display over other apps" onPress={() => SharingService.requestOverlayPermission()} />
            ) : null}
            <Text style={styles.hint}>{access.logs ? "✓" : "✗"} Log access (granted from your computer)</Text>
            {!access.logs ? (
              <Text selectable style={styles.code}>
                adb shell pm grant com.walon.sharing android.permission.READ_LOGS
              </Text>
            ) : null}
            {access.enabled && access.logActive === false ? (
              <>
                <Text style={styles.error}>
                  Android hasn&apos;t allowed log access in this session, so copies aren&apos;t being noticed. Tap below and choose “Allow
                  one-time access” when Android asks.
                </Text>
                <Button label="Ask for log access again" onPress={() => SharingService.setAutoClipboard(true)} />
              </>
            ) : access.enabled && access.logActive ? (
              <Text style={styles.hint}>✓ Watching for copies</Text>
            ) : null}
            <View style={styles.row}>
              <Text style={styles.title}>Copy automatically</Text>
              <Switch
                value={access.enabled}
                disabled={!(access.overlay && access.logs)}
                onValueChange={(on) => {
                  SharingService.setAutoClipboard(on);
                  refresh();
                }}
              />
            </View>
          </>
        ) : (
          <Text style={styles.hint}>
            This build does not include automatic copying: it does not ask for log access or display-over-apps at all. Build with `make
            apk-auto-clipboard` if you want it.
          </Text>
        )}
      </Card>

      <Button
        label="Finish and start"
        disabled={!status.paired}
        onPress={() => {
          SharingService.start();
          router.replace("/");
        }}
      />
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  container: { padding: spacing.screen, gap: 16 },
  row: { flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
  title: { fontSize: 17, fontWeight: "600" },
  hint: { color: colors.muted },
  error: { color: colors.danger },
  code: { fontFamily: "monospace", backgroundColor: colors.codeBackground, padding: 10, borderRadius: 8 },
});
