import { Redirect, router } from "expo-router";
import { useEffect, useRef } from "react";
import { StyleSheet, Text, View } from "react-native";
import { Button } from "../components/Button";
import { SharingService, useSharingStatus } from "../lib/sharing";
import { colors, spacing } from "../theme";

export default function Home() {
  const status = useSharingStatus();

  // Android only asks for log access while this app is on screen. If automatic clipboard is on but the service has no
  // access (it restarted in the background), restart it once per visit so the prompt appears. The native flags are read
  // once on arrival, not on every status event.
  const checked = useRef(false);
  useEffect(() => {
    if (checked.current || !status.paired) return;
    checked.current = true;
    const access = SharingService.clipboardAccess();
    if (access.supported && access.enabled && access.overlay && access.logs && access.logActive === false) {
      SharingService.setAutoClipboard(true);
    }
  }, [status.paired]);

  if (!status.paired) return <Redirect href="/setup" />;

  const state = status.connected ? `Connected to ${status.laptopName}` : status.running ? "Looking for your laptop…" : "Stopped";

  return (
    <View style={styles.container}>
      <Text style={styles.state}>{state}</Text>
      {status.error ? <Text style={styles.error}>{status.error}</Text> : null}
      <Text style={styles.hint}>
        Files from {status.laptopName} are saved in Downloads/{status.laptopName}.
      </Text>
      <Text style={styles.hint}>
        You can close this app. Share from any app with “Send to laptop”, or use the “Send clipboard” quick-settings tile. Transfers that
        are interrupted resume by themselves; progress and failures show in the notification.
      </Text>

      <Button label={status.running ? "Stop" : "Start"} onPress={() => (status.running ? SharingService.stop() : SharingService.start())} />
      <Button label="Send a folder to laptop" onPress={() => SharingService.pickFolderToSend()} />
      <Button label="Permissions and clipboard options" onPress={() => router.push("/permissions")} />
      <Button
        label="Unpair"
        danger
        onPress={async () => {
          await SharingService.unpair();
          router.replace("/setup");
        }}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, padding: spacing.screen, gap: spacing.gap },
  state: { fontSize: 22, fontWeight: "600" },
  error: { color: colors.danger },
  hint: { color: colors.muted },
});
