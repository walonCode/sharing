import { CameraView, useCameraPermissions } from "expo-camera";
import * as Device from "expo-device";
import { router } from "expo-router";
import { useEffect, useRef, useState } from "react";
import { Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { Button } from "../components/Button";
import { ErrorBanner } from "../components/ErrorBanner";
import { Card } from "../components/Step";
import { friendlyError } from "../lib/errors";
import { fingerprintPreview, parseInvite, type Invite } from "../lib/invite";
import { SharingService, useSharingStatus } from "../lib/sharing";
import { colors, spacing } from "../theme";

/** One-time pairing. Permissions are on their own screen (permissions.tsx). */
export default function Setup() {
  const status = useSharingStatus();
  const [cameraPermission, requestCamera] = useCameraPermissions();
  const [scanning, setScanning] = useState(false);
  const [link, setLink] = useState("");
  const [pending, setPending] = useState<{ raw: string; invite: Invite } | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  /** Shows who is about to be paired so the fingerprint can be compared with the laptop before anything is sent. */
  function review(raw: string) {
    const invite = parseInvite(raw);
    if (!invite) {
      setMessage(friendlyError(new Error("Not a sharing pairing link")));
      return;
    }
    setScanning(false);
    setMessage(null);
    setPending({ raw: raw.trim(), invite });
  }

  async function confirm() {
    if (!pending) return;
    setBusy(true);
    setMessage(null);
    try {
      await SharingService.pair(pending.raw, Device.deviceName ?? Device.modelName ?? "Android phone");
      setPending(null);
      router.replace("/permissions");
    } catch (e) {
      setMessage(friendlyError(e));
      setPending(null);
    } finally {
      setBusy(false);
    }
  }

  // Google's code scanner (its own UI with a viewfinder) reports results here.
  const reviewRef = useRef(review);
  useEffect(() => {
    reviewRef.current = review;
  });
  useEffect(() => {
    const sub = CameraView.onModernBarcodeScanned(({ data }) => reviewRef.current(data));
    return () => sub.remove();
  }, []);

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

  if (scanning) {
    return (
      <View style={styles.flex}>
        <CameraView style={styles.flex} barcodeScannerSettings={{ barcodeTypes: ["qr"] }} onBarcodeScanned={({ data }) => review(data)} />
        <View style={styles.overlay} pointerEvents="none">
          <View style={styles.frame} />
          <Text style={styles.scanText}>{message ?? "Point at the QR code on your laptop"}</Text>
        </View>
        <Pressable style={styles.cancel} onPress={() => setScanning(false)}>
          <Text style={styles.cancelText}>Cancel</Text>
        </Pressable>
      </View>
    );
  }

  return (
    <ScrollView contentContainerStyle={styles.container}>
      <Text style={styles.intro}>
        Set up once. Afterwards sharing and clipboard sync run in the background without opening this app. Received files are filed in a
        folder named after the sender: the laptop&apos;s name inside Downloads on this phone, and this phone&apos;s name on your laptop.
      </Text>

      {message ? <ErrorBanner title="Couldn't pair" message={message} onDismiss={() => setMessage(null)} /> : null}

      {pending ? (
        <Card title="Pair with this laptop?">
          <Text style={styles.strong}>{pending.invite.name}</Text>
          <Text style={styles.hint}>
            {pending.invite.host}:{pending.invite.port}
          </Text>
          <Text style={styles.hint}>Fingerprint (should match what your laptop shows):</Text>
          <Text selectable style={styles.code}>
            {fingerprintPreview(pending.invite.fingerprint)}
          </Text>
          {busy ? <Text style={styles.hint}>Waiting for the laptop to accept… (answer the prompt on the laptop)</Text> : null}
          <Button label={busy ? "Pairing…" : "Pair"} onPress={confirm} disabled={busy} />
          <Button label="Cancel" onPress={() => setPending(null)} disabled={busy} danger />
        </Card>
      ) : (
        <Card title="Pair with your laptop">
          <Text style={styles.hint}>On the laptop run `sharingd setup`, then scan the QR.</Text>
          <Button label="Scan QR" onPress={openScanner} />
          <TextInput
            style={styles.input}
            placeholder="or paste sharing://pair?… link"
            value={link}
            onChangeText={setLink}
            autoCapitalize="none"
            autoCorrect={false}
          />
          <Button label="Review link" onPress={() => review(link)} disabled={!link} />
        </Card>
      )}

      {status.paired ? (
        <Card title={`Paired with ${status.laptopName}`}>
          <Button label="Continue to permissions" onPress={() => router.replace("/permissions")} />
        </Card>
      ) : null}
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  container: { padding: spacing.screen, gap: 16 },
  intro: { fontSize: 16, color: "#444" },
  strong: { fontSize: 18, fontWeight: "700" },
  hint: { color: colors.muted },
  code: { fontFamily: "monospace", backgroundColor: colors.codeBackground, padding: 10, borderRadius: 8, fontSize: 16 },
  input: { borderWidth: 1, borderColor: "#ccc", borderRadius: 8, padding: 10 },
  overlay: { ...StyleSheet.absoluteFill, alignItems: "center", justifyContent: "center", gap: 20 },
  frame: { width: 260, height: 260, borderWidth: 4, borderColor: "white", borderRadius: 24 },
  scanText: {
    color: "white",
    fontWeight: "600",
    textAlign: "center",
    paddingHorizontal: 24,
    textShadowColor: "black",
    textShadowRadius: 6,
  },
  cancel: { position: "absolute", bottom: 40, alignSelf: "center", backgroundColor: "#000a", padding: 14, borderRadius: 10 },
  cancelText: { color: colors.white, fontWeight: "600" },
});
