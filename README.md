# sharing

AirDrop-style sharing between a Linux laptop and an Android phone over your local WiFi. No cloud, no accounts.

- **Files, folders, images, videos** both ways.
- **Clipboard sync** both ways. Laptop → phone is automatic. Phone → laptop is automatic after an optional one-time setup, or one tap without it.
- **Set up once, then forget it.** After pairing, both sides run in the background, start on boot, and reconnect by themselves, even when the laptop's IP address changes.

Contents: [Daily use](#daily-use) · [Install](#install) · [Automatic phone → laptop clipboard](#automatic-phone--laptop-clipboard-optional) · [Updating and removing](#updating-and-removing) · [Commands](#laptop-commands) · [Troubleshooting](#troubleshooting) · [How it works](#how-it-works) · [Limitations](#limitations)

## Daily use

| You want to | Do this |
| --- | --- |
| Send phone → laptop | Share from any app and pick **Send to laptop**. |
| Send a folder phone → laptop | Open the app, tap **Send a folder to laptop**. |
| Send laptop → phone | In your file manager: right-click → **Scripts → Send to phone** (Nautilus), or **Open With → Send to phone**. Or run `sharingd send photo.jpg Videos/`. A desktop notification confirms. |
| Copy laptop → phone | Copy as usual. It appears on the phone's clipboard. |
| Copy phone → laptop | Copy as usual if you set up the [automatic mode](#automatic-phone--laptop-clipboard-optional). Otherwise copy, then tap **Send clipboard to laptop** in the notification (expand it with the small arrow) or the **Send clipboard** quick-settings tile. |

**Where received files go.** Nothing to configure: each device files received items into a folder named after the sender.

| On | Folder |
| --- | --- |
| Laptop (from your phone) | `~/Downloads/Sharing/<phone name>/` |
| Phone (from your laptop) | `Downloads/<laptop name>/` |

Change the laptop's base folder with `sharingd setup --dir <path>`. Name collisions never overwrite (`photo (1).jpg`). Empty folders are not recreated on the phone.

## Install

You need a Linux laptop and an Android 10 or newer phone on the same WiFi.

### 1. Prerequisites

- **Laptop:** Go, `make`, a clipboard tool (`wl-clipboard` on Wayland, or `xclip` / `xsel` on X11), and mDNS (avahi, which most distributions already run). `notify-send` is optional, for send notifications.
- **Building the phone app:** [Bun](https://bun.sh), JDK 17–21 (Gradle breaks on newer JDKs), the Android SDK (`ANDROID_HOME`), and `adb`. On the phone, turn on **Developer options → USB debugging** and connect it by USB, accepting the "Allow USB debugging" prompt.
- **Firewall:** allow TCP port **53317** from your local network on the laptop.

### 2. Laptop

```bash
make service    # builds an optimised sharingd into ~/.local/bin and installs the systemd user service
```

This starts `sharingd` now, at every boot, restarts it if it ever crashes, and keeps it running after you log out. It also adds **Send to phone** to Nautilus and to the Open With menu (restart Nautilus with `nautilus -q` if the Scripts menu doesn't show it). Make sure `~/.local/bin` is on your `PATH`.

Without `make`: `cd api && go build -trimpath -ldflags="-s -w" -o ~/.local/bin/sharingd ./cmd/sharingd && sharingd install`.

### 3. Phone

```bash
make phone      # builds the minified arm64 release APK (about 40 MB) and installs it over adb
```

The first build takes 10–30 minutes. If your default Java is newer than 21: `make phone JAVA_HOME=/path/to/jdk21`. For a non-arm64 phone: `make phone ABIS=arm64-v8a,armeabi-v7a,x86_64`. The APK is signed with the debug key, which is fine for your own phone. `dist/sharing.apk` is also left on disk if you would rather copy it over yourself. (The app has a native background service, so it needs this build, not Expo Go.)

### 4. Pair

1. On the laptop run `sharingd setup`. It prints a QR code and a `sharing://pair?…` link (valid for 5 minutes, single use).
2. On the phone open **Sharing** and tap **Scan QR**. If the camera won't scan (a small terminal QR can be hard to read: zoom the terminal in), paste the whole link into the text box and tap **Pair with link**.
3. Step 2 in the app: tap **Allow notifications** and **Don't optimise battery**. This lets Android keep the background service alive.
4. Step 3 (optional): **Add "Send clipboard" tile** adds the one-tap clipboard button to Quick Settings.
5. Tap **Finish and start**.

Check it from the laptop with `sharingd peers`; your phone should say `online`. After this you never need to open the app again.

## Automatic phone → laptop clipboard (optional)

Android 10+ hides the clipboard from background apps, so out of the box phone → laptop copying takes one tap. This optional mode removes the tap by working around that restriction. It needs three one-time grants, one per-restart tap, and has trade-offs; read them first.

**What it does.** It watches the phone's system log for the entry Android writes whenever the clipboard changes, then briefly shows a 1-pixel invisible window, which gives the app the focus Android requires to read the clipboard, and sends the text to the laptop.

**Set it up.**

1. Connect the phone by USB and run (once; it survived every app update in testing, and is removed by an uninstall; check it again with the command in Troubleshooting if copying stops after a reboot):
   ```bash
   adb shell pm grant com.walon.sharing android.permission.READ_LOGS
   ```
2. In the app tap **Permissions and clipboard options**, then **Allow display over other apps**, and enable it for Sharing in the system screen.
3. Turn on the **Copy automatically** switch.
4. Android shows **"Allow Sharing to access all device logs?"**. Tap **Allow one-time access**.

**Every time the service restarts** (phone reboot, app update, the app being killed) Android asks again, and only while the app is on screen. Open **Sharing**, tap **Allow one-time access**, and you are done. The screen shows `✓ Watching for copies` when it is working, or a warning and an **Ask for log access again** button when it is not.

**Trade-offs.**

- `READ_LOGS` lets the app read every app's system logs. Remove it any time: `adb shell pm revoke com.walon.sharing android.permission.READ_LOGS`, or turn the switch off.
- Android 12+ shows its own "pasted from clipboard" notice each time the app reads a copy.
- It depends on log wording that Android and Samsung can change. It was verified on a Samsung phone running Android 16 (One UI logs `Copy toast is shown by <uid>`; stock Android logs `Denying clipboard access to com.walon.sharing`). On other phones it may not trigger, in which case use the one-tap notification button or tile.
- Text only; clips over 1 MiB are ignored.

## Updating and removing

| Task | Command |
| --- | --- |
| Update the laptop daemon (restarts the service) | `git pull && make service` |
| Update the phone app | `git pull && make phone` (keeps your pairing and settings) |
| Quick phone rebuild after small changes | `make phone-quick` (reuses the generated `mobile/android`) |
| Pair another phone | `sharingd setup` again on the laptop, then scan on that phone |
| Revoke a phone | `sharingd unpair <id or name>` (find it with `sharingd peers`) |
| Remove the laptop side | `make daemon-uninstall` (stops the service, removes the binary and file-manager entries) |
| Remove lingering at boot (optional) | `loginctl disable-linger` |
| Remove the phone side | Uninstall the app. Remove the `READ_LOGS` grant with it (automatic on uninstall). |

Config lives in `~/.config/sharing` (override with `SHARING_CONFIG_DIR`). Deleting it creates a new identity, so phones must pair again.

## Laptop commands

| Command | |
| --- | --- |
| `sharingd install` / `uninstall` | Add or remove the systemd user service and the file-manager "Send to phone" entries |
| `sharingd setup [--dir PATH] [--name NAME]` | Set the laptop's base receive folder or device name, then show a pairing QR |
| `sharingd pair` | Show a new pairing QR (valid 5 minutes) |
| `sharingd peers` | List paired phones and whether they are online |
| `sharingd unpair <id or name>` | Revoke a phone immediately |
| `sharingd send [--to PEER] <path>...` | Send files or folders to a phone |
| `sharingd run` | Run in the foreground (this is what the service does; a second copy refuses to start) |

`make help` lists the build targets: `test`, `service`, `setup`, `logs` (follow the daemon's log), `phone`, `phone-quick`, `apk`, `apk-quick`, `daemon-uninstall`, `clean`.

## Troubleshooting

**`sharingd peers` shows the phone as `offline`.** Open the Sharing app once (Android may have stopped the service). Check both devices are on the same WiFi (guest networks often isolate clients) and that port 53317 is open. On the laptop `systemctl --user status sharingd` and `make logs` show what the daemon sees.

**Pairing says "expired" or "already used".** Codes last 5 minutes and work once. Run `sharingd setup` for a new one.

**"Can't reach your laptop" when pairing.** Wrong network, daemon not running, or a firewall blocking port 53317. The link contains the laptop's current IP, so generate a fresh one if you changed networks.

**The QR won't scan.** Zoom the terminal in so the QR is large, or paste the `sharing://pair?…` link instead.

**Files from the phone arrive but I can't find them.** Look in `~/Downloads/Sharing/<phone name>/`. From the laptop to the phone, look in `Downloads/<laptop name>/` in the phone's file manager.

**Laptop → phone clipboard doesn't update.** It needs the phone service running (`sharingd peers` → online). Only text under 1 MiB syncs.

**Phone → laptop clipboard doesn't arrive.** Without the automatic mode, tap the notification's **Send clipboard to laptop** button (expand the notification) or the tile. With it: open the app and tap **Allow one-time access** if Android asks; check the screen shows `✓ Watching for copies`; confirm `adb shell dumpsys package com.walon.sharing | grep READ_LOGS` says `granted=true`.

**The Scripts → Send to phone menu is missing in Nautilus.** Run `nautilus -q` and reopen it. The entry is at `~/.local/share/nautilus/scripts/Send to phone`.

**The service stops after a while on the phone.** Battery savers (especially Samsung's "Sleeping apps") can kill it. Set the app to **Unrestricted** in Settings → Apps → Sharing → Battery, and keep "Don't optimise battery" enabled.

**`make phone` fails.** Check `adb devices` lists the phone as `device` (not `unauthorized`), that Gradle uses JDK 17–21 (`JAVA_HOME`), and `ANDROID_HOME` points at your SDK.

## How it works

The laptop is the only server; the phone is always a client.

1. The laptop serves HTTPS (TLS 1.2+) with a self-signed certificate and advertises `_sharing._tcp` over mDNS.
2. Pairing: the QR carries the laptop's address, the certificate fingerprint, and a single-use token. The phone pins the fingerprint and exchanges the token for a long-term device token.
3. The phone keeps an authenticated websocket open (last known address first, then mDNS, with backoff). Clipboard changes and "file ready" offers travel over it.
4. Phone → laptop transfers are streamed uploads. Laptop → phone transfers are offered over the websocket, then downloaded by the phone. Folders travel as tar.

```
api/      sharingd, the laptop daemon (Go)
mobile/   Android app: Expo UI plus a Kotlin foreground-service module
docs/     design plans
```

See [Security.md](Security.md) for the trust model and [Contributing.md](Contributing.md) to hack on it.

## Limitations

- Linux and Android 10 or newer only. Both devices must be on the same network.
- Text clipboard only. Items over 1 MiB are ignored.
- The file-manager integration targets Nautilus (and any file manager that supports Open With).
- The release APK is debug-signed, so it is for your own devices, not an app store.
- Automatic phone → laptop clipboard relies on a workaround that can break with Android or Samsung updates, and needs a tap after each service restart.

## License

[MIT](LICENSE) © walonCode
