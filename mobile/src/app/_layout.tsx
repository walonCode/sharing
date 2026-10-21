import { Stack } from "expo-router";

export default function RootLayout() {
  return (
    <Stack>
      <Stack.Screen name="index" options={{ title: "Sharing" }} />
      <Stack.Screen name="setup" options={{ title: "Set up" }} />
    </Stack>
  );
}
