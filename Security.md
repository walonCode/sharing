# Security

## Trust model

Sharing is meant for a laptop and a phone you own, on a network you trust. There is no cloud component.

- **Transport:** all traffic is HTTPS/WSS (TLS 1.2 or newer). The laptop generates a self-signed ECDSA certificate on first run. The phone does not trust any certificate authority; it accepts exactly one certificate, identified by the SHA-256 fingerprint it received through the pairing QR. Host names are deliberately not checked, because the laptop's IP changes. The pinned key is what authenticates it.
- **Pairing:** `sharingd pair`/`setup` creates a 256-bit random invite token that is valid for 5 minutes and works once. The phone exchanges it for a separate long-term device token. Only the SHA-256 of each device token is stored on the laptop. Anyone who can see the QR within those 5 minutes can pair, so do not photograph or share it.
- **Authorization:** every endpoint except `/pair` requires the device token. Tokens are compared in constant time.
- **Revocation:** `sharingd unpair <id or name>` deletes the device and closes its connection immediately. Re-pairing is required afterwards.
- **Control channel:** the CLI talks to the daemon through a unix socket with `0600` permissions in `$XDG_RUNTIME_DIR`, never over the network.

## What a paired phone can do

A paired phone is trusted. It can send files and clipboard text to the laptop and receive what you send. Uploads are always written into a sub-folder of the laptop's receive folder named after the phone, and the phone cannot choose another location, read file contents, or list files on the laptop.

## Optional automatic clipboard mode (phone → laptop)

Off by default. The app's manifest always *declares* two extra permissions, `READ_LOGS` and "Display over other apps", but they do nothing until you grant them and turn on **Copy automatically**.

- **`READ_LOGS`** is granted only through `adb` on a computer you control, and Android 13+ additionally asks you to confirm "Allow one-time access" each time the service restarts. While active, the app can read all apps' system logs. In this mode it only looks for clipboard-change lines (it matches two specific patterns and discards everything else), but the permission itself is broad. Revoke it any time with `adb shell pm revoke com.walon.sharing android.permission.READ_LOGS`.
- **Display over other apps** is used only to show a 1-pixel invisible window for a moment so Android lets the app read the clipboard.
- While the mode is on, **everything you copy on the phone is sent to the paired laptop**, including passwords copied from a password manager. Turn the switch off, or use the one-tap button instead, if that is a concern.

## Hardening already in place

- Uploaded names are reduced to a single path element; tar extraction rejects absolute paths, `..`, symlinks and hardlinks, extracts to a staging directory first, and publishes nothing if it fails.
- Received files never overwrite existing ones (`name (1).ext`).
- Offers are single use, tied to the phone they were made for, and expire after 10 minutes.
- Clipboard text over 1 MiB is ignored.
- A second `sharingd` refuses to start rather than taking over the running daemon's control socket.

## Known limitations

- **Anyone on your LAN can reach port 53317.** They cannot do anything without a token, but they can see that the service exists (it is advertised over mDNS). Do not use this on untrusted networks such as public WiFi.
- Content is protected in transit only. Received files are stored unencrypted in the receive folder.
- The phone keeps its device token in the app's private storage (`SharedPreferences`), not in the hardware-backed Keystore. Rooted phones or backups of app data could expose it.
- Clipboard sync sends everything you copy on the laptop to paired phones (and, with the optional automatic mode, everything you copy on the phone to the laptop), including passwords if you copy them. Unpair or stop the service if that is a concern.
- If a certificate is ever lost or regenerated (deleting `~/.config/sharing`), phones must re-pair.

## Reporting a vulnerability

Please do not open a public issue for security problems. Use GitHub's private reporting: on the repository page choose **Security → Report a vulnerability**. Include what you found, how to reproduce it, and the version or commit. You can expect an acknowledgement within a week; fixes are released as soon as they are verified.
