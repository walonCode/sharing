# sharing

AirDrop-style sharing between a Linux laptop and an Android phone over your local WiFi. No cloud, no accounts.

- **Files, folders, images, videos** both ways.
- **Clipboard sync**: copy text on the laptop and it is on the phone's clipboard automatically.
- **Set up once, then forget it.** You open the phone app a single time to pair. After that both sides run in the background, start on boot, and reconnect by themselves, even when the laptop's IP address changes.

## How you use it

| You want to | Do this |
| --- | --- |
| Send phone → laptop | Share from any app and pick **Send to laptop**. |
| Send a folder phone → laptop | Open the app, **Send a folder to laptop**. |
| Send laptop → phone | In your file manager: right-click → **Scripts → Send to phone** (Nautilus) or **Open With → Send to phone**. Or `sharingd send photo.jpg Videos/`. A desktop notification confirms. |
| Copy laptop → phone | Copy as usual; it appears on the phone. |
| Copy phone → laptop | Tap the **Send clipboard** quick-settings tile, or share text to **Send to laptop**. |

Android 10+ does not let background apps read the clipboard, so phone → laptop clipboard is a deliberate tap rather than automatic. Laptop → phone is automatic.

## Install

### Quick start with make

```bash
make service   # build an optimised sharingd into ~/.local/bin and install the service
make phone     # build the minified release APK and install it on the USB-connected phone
make setup     # show the pairing QR
```

`make help` lists everything. The APK is built for arm64 phones only (about 40 MB); use `make phone ABIS=arm64-v8a,armeabi-v7a,x86_64` for other CPUs. Gradle needs JDK 17–21: set `JAVA_HOME=/path/to/jdk21` if your default is newer. The release APK is signed with the debug key, which is fine for installing on your own phone.

### Laptop (Linux)

Requires Go, a clipboard tool (`wl-clipboard` on Wayland, or `xclip`/`xsel` on X11), and mDNS on your network (avahi).

```bash
cd api
go build -o ~/.local/bin/sharingd ./cmd/sharingd
sharingd install   # systemd user service: starts at boot, restarts forever, survives logout
sharingd setup     # prints a pairing QR
```

Allow TCP port 53317 from your local network in your firewall.

### Phone (Android)

The app includes a native background service, so it needs a development build (Expo Go is not enough):

```bash
cd mobile
bun install
bunx expo run:android      # phone connected over USB with debugging on
```

Open the app and follow the two setup steps: scan the QR, then allow notifications and background running. Tap **Finish and start**.

## Laptop commands

| Command | |
| --- | --- |
| `sharingd install` / `uninstall` | Add or remove the systemd user service and the file-manager "Send to phone" entries |
| `sharingd setup [--dir PATH] [--name NAME]` | Set the receive folder or device name, show a pairing QR |
| `sharingd pair` | Show a new pairing QR (valid 5 minutes) |
| `sharingd peers` | List paired phones and whether they are online |
| `sharingd unpair <id or name>` | Revoke a phone immediately |
| `sharingd send [--to PEER] <path>...` | Send files or folders to a phone |
| `sharingd run` | Run in the foreground (what the service does) |

Config lives in `~/.config/sharing` (override with `SHARING_CONFIG_DIR`).

## How it works

The laptop is the only server; the phone is always a client.

1. The laptop serves HTTPS with a self-signed certificate and advertises `_sharing._tcp` over mDNS.
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

- Linux and Android only. Both devices must be on the same network (client isolation on guest WiFi blocks it).
- Text clipboard only. Items over 1 MiB are ignored.
- The Android app has been compiled and unit-tested but is young; expect rough edges.

## License

[MIT](LICENSE) © walonCode
