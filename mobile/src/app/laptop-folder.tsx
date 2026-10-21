import { router } from "expo-router";
import { useCallback, useEffect, useState } from "react";
import { FlatList, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { createFolder, type FolderListing, listFolders, setReceiveFolder } from "../lib/laptop";

/** Browse the laptop's folders from the phone and choose where received files land. */
export default function LaptopFolder() {
  const [listing, setListing] = useState<FolderListing | null>(null);
  const [home, setHome] = useState("");
  const [newName, setNewName] = useState("");
  const [error, setError] = useState<string | null>(null);

  const open = useCallback(async (path: string) => {
    try {
      const next = await listFolders(path);
      setListing(next);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    let cancelled = false;
    listFolders("").then(
      (first) => {
        if (cancelled) return;
        setListing(first);
        setHome(first.path); // the first listing is the laptop's home
      },
      (e) => !cancelled && setError(e instanceof Error ? e.message : String(e)),
    );
    return () => {
      cancelled = true;
    };
  }, []);

  async function choose(path: string) {
    try {
      await setReceiveFolder(path);
      router.back();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function add() {
    if (!listing || !newName.trim()) return;
    try {
      const path = await createFolder(listing.path, newName.trim());
      setNewName("");
      await open(path);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  if (!listing) {
    return (
      <View style={styles.container}>
        <Text>{error ?? "Loading laptop folders…"}</Text>
        {error ? <Button label="Retry" onPress={() => open("")} /> : null}
      </View>
    );
  }

  const shown = listing.path === home ? "~" : listing.path.replace(home, "~");

  return (
    <View style={styles.container}>
      <Text style={styles.path}>{shown}</Text>
      {error ? <Text style={styles.error}>{error}</Text> : null}
      <Button label={`Save received files in ${shown}`} onPress={() => choose(listing.path)} />
      <View style={styles.row}>
        <TextInput style={[styles.input, styles.flex]} placeholder="New folder name" value={newName} onChangeText={setNewName} />
        <Button label="Create" onPress={add} disabled={!newName.trim()} />
      </View>
      <FlatList
        data={listing.parent ? ["..", ...listing.dirs] : listing.dirs}
        keyExtractor={(d) => d}
        renderItem={({ item }) => (
          <Pressable style={styles.item} onPress={() => open(item === ".." ? listing.parent : `${listing.path}/${item}`)}>
            <Text style={styles.itemText}>{item === ".." ? "↑ Up" : `📁 ${item}`}</Text>
          </Pressable>
        )}
        ListEmptyComponent={<Text style={styles.hint}>No subfolders</Text>}
      />
    </View>
  );
}

function Button({ label, onPress, disabled }: { label: string; onPress: () => void; disabled?: boolean }) {
  return (
    <Pressable onPress={onPress} disabled={disabled} style={[styles.button, disabled && styles.disabled]}>
      <Text style={styles.buttonText}>{label}</Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, padding: 16, gap: 10 },
  flex: { flex: 1 },
  row: { flexDirection: "row", gap: 8, alignItems: "center" },
  path: { fontSize: 18, fontWeight: "600" },
  hint: { color: "#666", padding: 12 },
  error: { color: "#c0392b" },
  input: { borderWidth: 1, borderColor: "#ccc", borderRadius: 8, padding: 10 },
  item: { paddingVertical: 14, borderBottomWidth: StyleSheet.hairlineWidth, borderColor: "#ccc" },
  itemText: { fontSize: 16 },
  button: { backgroundColor: "#208AEF", padding: 14, borderRadius: 10, alignItems: "center" },
  disabled: { opacity: 0.4 },
  buttonText: { color: "white", fontWeight: "600" },
});
