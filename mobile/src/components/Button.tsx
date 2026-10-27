import { Pressable, StyleSheet, Text } from "react-native";
import { colors } from "../theme";

type Props = { label: string; onPress: () => void; disabled?: boolean; danger?: boolean };

export function Button({ label, onPress, disabled, danger }: Props) {
  return (
    <Pressable
      onPress={onPress}
      disabled={disabled}
      accessibilityRole="button"
      style={[styles.button, danger && styles.danger, disabled && styles.disabled]}
    >
      <Text style={styles.label}>{label}</Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  button: { backgroundColor: colors.primary, padding: 14, borderRadius: 10, alignItems: "center" },
  danger: { backgroundColor: colors.danger },
  disabled: { opacity: 0.4 },
  label: { color: colors.white, fontWeight: "600" },
});
