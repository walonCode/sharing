# sharing

AirDrop-style sharing between a Linux laptop and an Android phone over WiFi: files, folders, images, videos, and an automatically synced clipboard. Open the phone app once to set it up; after that both sides run in the background.

- `api/` — `sharingd`, the laptop daemon (Go).
- `mobile/` — the Android app (Expo plus a native foreground service).
- `docs/` — build plans and design notes.

## Laptop setup

Requires Go, `wl-clipboard` (Wayland) or `xclip`/`xsel` (X11), and avahi/mDNS on the network.

```bash
cd api
go build -o ~/.local/bin/sharingd ./cmd/sharingd
sharingd install                          # systemd user service, starts at login
sharingd setup --dir ~/Downloads/Sharing  # receive folder, then shows a pairing QR
loginctl enable-linger $USER              # optional: keep running while logged out
```

Other commands: `sharingd pair` (new QR), `sharingd peers`, `sharingd send [--to peer] <path>...`, `sharingd uninstall`.

Config lives in `~/.config/sharing` (override with `SHARING_CONFIG_DIR`). The daemon listens on TCP 53317; allow it through your firewall for the local network.

## How it works

The laptop is the only server. It advertises `_sharing._tcp` over mDNS, serves HTTPS with a self-signed certificate, and the phone pins that certificate's fingerprint, which it learns from the pairing QR. The QR also carries a single-use token (5 minute lifetime) that the phone exchanges for a long-term device token. See `docs/plan1.md` and `docs/plan2.md`.
