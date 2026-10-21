# Plan 2: Clipboard, packaging, and Android

Depends on plan 1 (pairing, websocket hub, transfer API).

## Step 4: Clipboard sync (Linux side)

1. `protocol`: add `clip` message `{id, mime: text/plain, data, ts}`.
2. `internal/clipboard`: Wayland via `wl-paste --watch` / `wl-copy`, X11 fallback via `xclip`. Watcher emits local changes; setter writes remote changes.
3. Loop prevention: keep hash of last value set from remote and ignore the matching local change event; last-writer-wins by timestamp.
4. Tests with a fake clipboard backend: echo suppression, ordering, size cap (1 MiB text; images later).

## Step 5: Daemon packaging and CLI

1. `sharingd run` as a systemd user unit (`sharingd.service`, `Restart=always`, `WantedBy=default.target`); `sharingd install` writes and enables it.
2. `sharingd setup`: choose receive folder, print pairing QR.
3. Document install in README.

## Step 6: Android background service

1. Local Expo module `mobile/modules/sharing-service` (Kotlin; its own `AndroidManifest.xml` merges permissions, service, boot receiver, share target and tile into the app, so no config plugin is needed): a foreground service (persistent notification, `FOREGROUND_SERVICE_DATA_SYNC`), started on boot via `BOOT_COMPLETED` receiver and restarted if killed.
2. Inside the service: NsdManager browse for `_sharing._tcp`, OkHttp with pinned fingerprint, websocket with reconnect and backoff, upload/download of files and folders (tar) into the chosen folder via Storage Access Framework tree URI.
3. Clipboard: Android 10+ blocks background clipboard reads. Approach: a tiny invisible-activity trigger / share-sheet target ("Send to laptop") plus an accessibility-free best effort listener while the foreground service holds focus-less access; incoming clips are always written. Document the limitation.
4. Share sheet entry (`ACTION_SEND`, `ACTION_SEND_MULTIPLE`) so sharing works without opening the app.

## Step 7: Android setup UI (Expo)

One-time screens only: scan/enter pairing invite, pick receive folder (SAF picker), grant notification and battery-optimization exemption, start service. After setup the app never needs to open.

## Done criteria

Lint + typecheck clean for `mobile/`, Go tests pass, end-to-end manual test on a real device; all dev services and emulators stopped afterwards.

## Branches/PRs (kept after merge)

`feat/clipboard`, `feat/packaging`, `feat/android-service`, `feat/android-setup-ui`.

## Implementation notes

- The service uses `foregroundServiceType="connectedDevice"`, not `dataSync`: Android 15 forbids starting dataSync foreground services from `BOOT_COMPLETED`.
- Android 10+ blocks background clipboard reads. Phone to laptop clipboard is therefore explicit: the "Send clipboard" quick-settings tile, or sharing text to "Send to laptop". Laptop to phone clipboard is applied automatically (writes are allowed in the background).
- Connection order: last known address, then mDNS. Reconnects with 1s to 30s backoff and immediately on network changes.
- Status: Kotlin compiles against SDK 58 and the merged manifest builds; not yet exercised on a physical device.
