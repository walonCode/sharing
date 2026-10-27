import { StyleSheet, Text, View } from "react-native";
import { colors } from "../theme";

type Props = {
  n: number;
  title: string;
  done: boolean;
  doneText: string;
  disabled?: boolean;
  children: React.ReactNode;
};

/** A numbered setup card that collapses to a one-line summary once done. */
export function Step({ n, title, done, doneText, disabled, children }: Props) {
  return (
    <View style={[styles.card, disabled && styles.disabled]} pointerEvents={disabled ? "none" : "auto"}>
      <Text style={styles.title}>
        {done ? "✓" : n}. {title}
      </Text>
      {done ? <Text style={styles.hint}>{doneText}</Text> : children}
    </View>
  );
}

/** Same card without the numbering, for optional sections. */
export function Card({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <View style={styles.card}>
      <Text style={styles.title}>{title}</Text>
      {children}
    </View>
  );
}

const styles = StyleSheet.create({
  card: { gap: 8, padding: 16, borderRadius: 12, borderWidth: 1, borderColor: colors.border },
  title: { fontSize: 17, fontWeight: "600" },
  hint: { color: colors.muted },
  disabled: { opacity: 0.4 },
});
