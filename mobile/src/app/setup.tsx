import { CameraView, useCameraPermissions } from "expo-camera";
import * as Device from "expo-device";
import { router, useFocusEffect } from "expo-router";
import { useCallback, useState } from "react";
import { PermissionsAndroid, Platform, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { parseInvite } from "../lib/invite";
import { getReceiveFolder } from "../lib/laptop";
import { SharingService, useSharingStatus } from "../lib/sharing";

/** One-time setup: pair, choose the receive folder, allow background running. */
export default function Setup() {
  const status = useSharingStatus();
  const [cameraPermission, requestCamera] = useCameraPermissions();
  const [scanning, setScanning] = useState(false);
  const [link, setLink] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [notificationsOk, setNotificationsOk] = useState(false);
  const [batteryOk, setBatteryOk] = useState(() => SharingService.isBatteryOptimizationIgnored());

  async function pair(raw: string) {
    const invite = parseInvite(raw);
    if (!invite) {
      setMessage("That is not a sharing pairing link. Run `sharingd setup` on your laptop and scan its QR.");
      return;
    }
    setBusy(true);
    setMessage(null);
    try {
      await SharingService.pair(raw.trim(), Device.deviceName ?? Device.modelName ?? "Android phone");
      setScanning(false);
    } catch (e) {
      setMessage(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function allowNotifications() {
    if (Platform.OS !== "android") return;
    const result = await PermissionsAndroid.request(PermissionsAndroid.PERMISSIONS.POST_NOTIFICATIONS);
    setNotificationsOk(result === PermissionsAndroid.RESULTS.GRANTED || Platform.Version < 33);
  }

  async function openScanner() {
    if (!cameraPermission?.granted && !(await requestCamera()).granted) {
      setMessage("Camera permission is needed to scan. You can paste the link instead.");
      return;
    }
    setScanning(true);
  }

  // Refresh when returning from the laptop folder browser.
  const [laptopFolder, setLaptopFolder] = useState<string | null>(null);
  useFocusEffect(
    useCallback(() => {
      if (!status.paired) return;
      getReceiveFolder().then(setLaptopFolder, () => setLaptopFolder(null));
    }, [status.paired]),
  );

  const ready = status.paired && !!status.folder && laptopFolder !== null;

  if (scanning) {
    return (
      <View style={styles.flex}>
        <CameraView
          style={styles.flex}
          barcodeScannerSettings={{ barcodeTypes: ["qr"] }}
          onBarcodeScanned={busy ? undefined : ({ data }) => pair(data)}
        />
        <Pressable style={styles.cancel} onPress={() => setScanning(false)}>
          <Text style={styles.buttonText}>Cancel</Text>
        </Pressable>
      </View>
    );
  }

  return (
    <ScrollView contentContainerStyle={styles.container}>
      <Text style={styles.intro}>
        Set up once. Afterwards sharing and clipboard sync run in the background without opening this app.
      </Text>

      <Step n={1} title="Pair with your laptop" done={status.paired} doneText={`Paired with ${status.laptopName}`}>
        <Text style={styles.hint}>On the laptop run `sharingd setup`, then scan the QR.</Text>
        <Button label="Scan QR" onPress={openScanner} disabled={busy} />
        <TextInput
          style={styles.input}
          placeholder="or paste sharing://pair?… link"
          value={link}
          onChangeText={setLink}
          autoCapitalize="none"
          autoCorrect={false}
        />
        <Button label={busy ? "Pairing…" : "Pair with link"} onPress={() => pair(link)} disabled={busy || !link} />
      </Step>

      <Step n={2} title="Choose where received files go" done={!!status.folder} doneText={`On this phone: ${status.folder}`} disabled={!status.paired}>
        <Text style={styles.hint}>Files sent from the laptop are saved here on the phone.</Text>
        <Button label="Choose phone folder" onPress={() => SharingService.pickReceiveFolder()} />
      </Step>

      <Step n={3} title="Choose the laptop's folder" done={laptopFolder !== null} doneText={`On the laptop: ${laptopFolder}`} disabled={!status.paired}>
        <Text style={styles.hint}>Files sent from the phone are saved here on the laptop. You browse the laptop from here.</Text>
        <Button label="Browse laptop folders" onPress={() => router.push("/laptop-folder")} />
      </Step>

      <Step n={4} title="Allow background running" done={notificationsOk && batteryOk} doneText="Allowed" disabled={!status.paired}>
        <Button label="Allow notifications" onPress={allowNotifications} />
        <Button
          label="Don't optimise battery"
          onPress={() => {
            SharingService.requestIgnoreBatteryOptimization();
            setTimeout(() => setBatteryOk(SharingService.isBatteryOptimizationIgnored()), 1500);
          }}
        />
      </Step>

      {message ? <Text style={styles.error}>{message}</Text> : null}

      <Button
        label="Finish and start"
        disabled={!ready}
        onPress={() => {
          SharingService.start();
          router.replace("/");
        }}
      />
    </ScrollView>
  );
}

function Step({
  n,
  title,
  done,
  doneText,
  disabled,
  children,
}: {
  n: number;
  title: string;
  done: boolean;
  doneText: string;
  disabled?: boolean;
  children: React.ReactNode;
}) {
  return (
    <View style={[styles.step, disabled && styles.disabled]} pointerEvents={disabled ? "none" : "auto"}>
      <Text style={styles.stepTitle}>
        {done ? "✓" : n}. {title}
      </Text>
      {done ? <Text style={styles.hint}>{doneText}</Text> : children}
    </View>
  );
}

function Button({ label, onPress, disabled }: { label: string; onPress: () => void; disabled?: boolean }) {
  return (
    <Pressable onPress={onPress} disabled={disabled} style={[styles.button, disabled && styles.buttonDisabled]}>
      <Text style={styles.buttonText}>{label}</Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  container: { padding: 24, gap: 16 },
  intro: { fontSize: 16, color: "#444" },
  step: { gap: 8, padding: 16, borderRadius: 12, borderWidth: 1, borderColor: "#ddd" },
  disabled: { opacity: 0.4 },
  stepTitle: { fontSize: 17, fontWeight: "600" },
  hint: { color: "#666" },
  error: { color: "#c0392b" },
  input: { borderWidth: 1, borderColor: "#ccc", borderRadius: 8, padding: 10 },
  button: { backgroundColor: "#208AEF", padding: 14, borderRadius: 10, alignItems: "center" },
  buttonDisabled: { opacity: 0.4 },
  buttonText: { color: "white", fontWeight: "600" },
  cancel: { position: "absolute", bottom: 40, alignSelf: "center", backgroundColor: "#000a", padding: 14, borderRadius: 10 },
});
