import { CameraView, useCameraPermissions } from "expo-camera";
import * as Device from "expo-device";
import { router } from "expo-router";
import { useEffect, useRef, useState } from "react";
import { PermissionsAndroid, Platform, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { ErrorBanner } from "../components/ErrorBanner";
import { friendlyError } from "../lib/errors";
import { parseInvite } from "../lib/invite";
import { SharingService, useSharingStatus } from "../lib/sharing";

/** One-time setup: pair, then allow background running. */
export default function Setup() {
  const status = useSharingStatus();
  const [cameraPermission, requestCamera] = useCameraPermissions();
  const [scanning, setScanning] = useState(false);
  const [link, setLink] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [notificationsOk, setNotificationsOk] = useState(false);
  const [batteryOk, setBatteryOk] = useState(() => SharingService.isBatteryOptimizationIgnored());

  const pairRef = useRef<(raw: string) => void>(() => {});

  async function pair(raw: string) {
    const invite = parseInvite(raw);
    if (!invite) {
      setMessage(friendlyError(new Error("Not a sharing pairing link")));
      return;
    }
    setBusy(true);
    setMessage(null);
    try {
      await SharingService.pair(raw.trim(), Device.deviceName ?? Device.modelName ?? "Android phone");
      setScanning(false);
    } catch (e) {
      setMessage(friendlyError(e));
    } finally {
      setBusy(false);
    }
  }

  // Google's code scanner (its own UI with a viewfinder) reports results here.
  useEffect(() => {
    const sub = CameraView.onModernBarcodeScanned(({ data }) => {
      pairRef.current(data);
    });
    return () => sub.remove();
  }, []);

  async function allowNotifications() {
    if (Platform.OS !== "android") return;
    const result = await PermissionsAndroid.request(PermissionsAndroid.PERMISSIONS.POST_NOTIFICATIONS);
    setNotificationsOk(result === PermissionsAndroid.RESULTS.GRANTED || Platform.Version < 33);
  }

  useEffect(() => {
    pairRef.current = pair;
  });

  async function openScanner() {
    if (!cameraPermission?.granted && !(await requestCamera()).granted) {
      setMessage("Camera permission is needed to scan. You can paste the link instead.");
      return;
    }
    if (CameraView.isModernBarcodeScannerAvailable) {
      try {
        await CameraView.launchScanner({ barcodeTypes: ["qr"] });
        return;
      } catch {
        // fall through to the in-app camera
      }
    }
    setScanning(true);
  }

  const ready = status.paired;

  if (scanning) {
    return (
      <View style={styles.flex}>
        <CameraView
          style={styles.flex}
          barcodeScannerSettings={{ barcodeTypes: ["qr"] }}
          onBarcodeScanned={busy ? undefined : ({ data }) => pair(data)}
        />
        <View style={styles.overlay} pointerEvents="none">
          <View style={styles.frame} />
          <Text style={styles.scanText}>{busy ? "Pairing…" : (message ?? "Point at the QR code on your laptop")}</Text>
        </View>
        <Pressable style={styles.cancel} onPress={() => setScanning(false)}>
          <Text style={styles.buttonText}>Cancel</Text>
        </Pressable>
      </View>
    );
  }

  return (
    <ScrollView contentContainerStyle={styles.container}>
      <Text style={styles.intro}>
        Set up once. Afterwards sharing and clipboard sync run in the background without opening this app. Received files are filed in a folder named after the sender: your laptop's name inside Downloads on this phone, and this phone's name on your laptop.
      </Text>

      {busy ? <Text style={styles.hint}>Pairing…</Text> : null}
      {message ? <ErrorBanner title="Couldn't pair" message={message} onDismiss={() => setMessage(null)} /> : null}

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

      <Step n={2} title="Allow background running" done={notificationsOk && batteryOk} doneText="Allowed" disabled={!status.paired}>
        <Button label="Allow notifications" onPress={allowNotifications} />
        <Button
          label="Don't optimise battery"
          onPress={() => {
            SharingService.requestIgnoreBatteryOptimization();
            setTimeout(() => setBatteryOk(SharingService.isBatteryOptimizationIgnored()), 1500);
          }}
        />
      </Step>

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
  overlay: { ...StyleSheet.absoluteFill, alignItems: "center", justifyContent: "center", gap: 20 },
  frame: { width: 260, height: 260, borderWidth: 4, borderColor: "white", borderRadius: 24 },
  scanText: { color: "white", fontWeight: "600", textAlign: "center", paddingHorizontal: 24, textShadowColor: "black", textShadowRadius: 6 },
  cancel: { position: "absolute", bottom: 40, alignSelf: "center", backgroundColor: "#000a", padding: 14, borderRadius: 10 },
});
