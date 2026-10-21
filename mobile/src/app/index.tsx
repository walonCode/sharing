import { Redirect, router } from "expo-router";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { SharingService, useSharingStatus } from "../lib/sharing";

export default function Home() {
  const status = useSharingStatus();

  if (!status.paired || !status.folder) return <Redirect href="/setup" />;

  const state = status.connected
    ? `Connected to ${status.laptopName}`
    : status.running
      ? "Looking for your laptop…"
      : "Stopped";

  return (
    <View style={styles.container}>
      <Text style={styles.state}>{state}</Text>
      {status.error ? <Text style={styles.error}>{status.error}</Text> : null}
      <Text style={styles.hint}>Saving to: {status.folder}</Text>
      <Text style={styles.hint}>
        You can close this app. Share from any app with “Send to laptop”, or use the “Send clipboard” quick-settings tile.
      </Text>

      <Button
        label={status.running ? "Stop" : "Start"}
        onPress={() => (status.running ? SharingService.stop() : SharingService.start())}
      />
      <Button label="Send a folder to laptop" onPress={() => SharingService.pickFolderToSend()} />
      <Button label="Change receive folder" onPress={() => SharingService.pickReceiveFolder()} />
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

function Button({ label, onPress, danger }: { label: string; onPress: () => void; danger?: boolean }) {
  return (
    <Pressable onPress={onPress} style={[styles.button, danger && styles.danger]}>
      <Text style={styles.buttonText}>{label}</Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, padding: 24, gap: 12 },
  state: { fontSize: 22, fontWeight: "600" },
  error: { color: "#c0392b" },
  hint: { color: "#666" },
  button: { backgroundColor: "#208AEF", padding: 14, borderRadius: 10, alignItems: "center" },
  danger: { backgroundColor: "#c0392b" },
  buttonText: { color: "white", fontWeight: "600" },
});
