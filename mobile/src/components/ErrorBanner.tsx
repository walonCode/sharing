import { Pressable, StyleSheet, Text, View } from "react-native";
import { colors } from "../theme";

/** A visible, dismissible error card. Keep it near the top so it is never scrolled out of view. */
export function ErrorBanner({ title, message, onDismiss }: { title: string; message: string; onDismiss: () => void }) {
  return (
    <View style={styles.card} accessibilityRole="alert">
      <Text style={styles.title}>{title}</Text>
      <Text style={styles.message}>{message}</Text>
      <Pressable onPress={onDismiss} hitSlop={8}>
        <Text style={styles.dismiss}>Dismiss</Text>
      </Pressable>
    </View>
  );
}

const styles = StyleSheet.create({
  card: { backgroundColor: "#fdecea", borderColor: colors.danger, borderWidth: 1, borderRadius: 12, padding: 14, gap: 6 },
  title: { color: "#8e2a20", fontWeight: "700", fontSize: 16 },
  message: { color: "#5b1a14", lineHeight: 20 },
  dismiss: { color: colors.danger, fontWeight: "600", marginTop: 4 },
});
