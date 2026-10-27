# Security

## Trust model

Sharing is meant for a laptop and a phone you own, on a network you trust. There is no cloud component.

- **Transport:** all traffic is HTTPS/WSS (TLS 1.2 or newer). The laptop generates a self-signed ECDSA certificate on first run. The phone does not trust any certificate authority; it accepts exactly one certificate, identified by the SHA-256 fingerprint it received through the pairing QR. Host names are deliberately not checked, because the laptop's IP changes. The pinned key is what authenticates it. A different certificate at the same address makes the connection fail before any request (and so any `Authorization` header) is sent.
- **Pairing:** `sharingd pair`/`setup` creates a 256-bit random invite token that is valid for 5 minutes and works once. The phone exchanges it for a separate long-term device token. Only the SHA-256 of each device token is stored on the laptop. By default the laptop then asks on the terminal, `Pixel 8 (device id) wants to pair, accept? [y/N]`, before issuing the token, so someone who photographed the QR still needs you to say yes (`--yes` skips the question for scripts). If the response to a pairing request is lost, the same device may repeat it within 2 minutes and receives a fresh token (the old one is replaced); a different device cannot reuse the invite.
- **Authorization:** every endpoint except `/pair` requires the device token. Tokens are compared in constant time. Failed authentications are logged with the client address (never the token) and, after 10 failures in a minute from one address, that address is refused with 429 until the minute is over.
- **Revocation and rotation:** `sharingd unpair <id or name>` deletes the device, closes its connection, and deletes its offers and unfinished uploads. `sharingd rotate <id or name>` issues a new device token over the live websocket; the old token keeps working for 60 seconds so the phone can store the new one. `sharingd peers` shows when each phone was last seen, which is how you notice a token in use that should not be.
- **Control channel:** the CLI talks to the daemon through a unix socket in a private `0700` directory (`$XDG_RUNTIME_DIR/sharing/`, or `~/.local/state/sharing/` when there is no runtime directory; never `/tmp`). The directory must be a real directory owned by you; the socket is created under `umask 077`.

## Threat model

| Asset | Where it lives |
| --- | --- |
| Files in transit and at rest | the receive folder on the laptop, `Downloads/<laptop>` on the phone, the spool and partial files while a transfer is queued |
| Clipboard text | memory, websocket |
| Device tokens | laptop: SHA-256 hashes in `peers.json`; phone: AES-GCM ciphertext under a non-exportable Android Keystore key |
| Certificate private key | `~/.config/sharing` (mode 0600) |

| Attacker | Can do | Mitigation |
| --- | --- | --- |
| Stranger on the same LAN | reach port 53317, see the mDNS advertisement, try tokens, hold connections open | pinned TLS, 256-bit tokens, hashed storage, failure throttling, header/handshake/idle timeouts, request size limits |
| Someone who saw the QR | pair within 5 minutes | single-use invite, laptop-side confirmation, `peers` shows new devices |
| Malicious local app on the laptop | talk to the control socket, read the config | private `0700` socket directory; config files are 0600 (a process running as you can still read them; that is outside what this tool can defend) |
| Malicious local app on the phone | send a crafted share intent, read app data | share target accepts only `content://` URIs and refuses this app's own provider and private paths; app data is excluded from backups; the token is encrypted by a key apps cannot export |
| Lost or rooted phone | use the token | `sharingd unpair` / `rotate`; Keystore wrapping does not protect against root |
| Malicious paired peer | fill the disk, send hostile archives or paths, flood connections | upload and entry caps, free-space reserve, per-peer concurrency and session caps, path validation, no overwrite, uploads confined to the peer's own folder |

## What a paired phone can do

A paired phone is trusted. It can send files and clipboard text to the laptop and receive what you send. Uploads are always written into a sub-folder of the laptop's receive folder named after the phone, and the phone cannot choose another location, read file contents, or list files on the laptop. It can only touch its own upload sessions and its own offers (other ids answer 404).

## Limits (settings.json)

Defaults protect the laptop from a buggy or misbehaving peer. They are plain keys in `~/.config/sharing/settings.json`:

| Key | Default | Meaning |
| --- | --- | --- |
| `maxUploadBytes` | 64 GiB | largest single upload or archive (0 = only free disk space limits it) |
| `maxEntries` | 200 000 | entries in a folder transfer |
| `maxTransfersPerPeer` | 4 | concurrent transfers per phone (more get 503 with `Retry-After`) |
| `minFreeBytes` | 1 GiB | free space that must remain after a transfer (otherwise 507) |
| `maxPartialBytesPerPeer` | 256 GiB | declared size of one phone's unfinished uploads together |
| `confirmPairing` | true | ask on the terminal before accepting a phone |

Fixed limits: 16 KiB of request headers, 10 s to send them (net/http applies the same limit to the TLS handshake, so a client that connects and says nothing is cut too), 30 s without a byte during a transfer, 64 unfinished uploads per phone, 8 MiB per websocket frame, clipboard text 1 MiB. Unfinished upload sessions are deleted after 72 hours idle; finished ones keep answering for 10 minutes so a lost response cannot create a duplicate file; `.partial-*` leftovers not belonging to a live session are swept at start-up.

## Optional automatic clipboard mode (phone → laptop)

Off by default, and **not present in the default APK**. The default build neither declares nor requests `READ_LOGS` or "Display over other apps". The opt-in build (`make apk-auto-clipboard`) declares them; they still do nothing until you grant them and turn on **Copy automatically**.

- **`READ_LOGS`** is granted only through `adb` on a computer you control, and Android 13+ additionally asks you to confirm "Allow one-time access" each time the service restarts. While active, the app can read all apps' system logs. In this mode it only looks for clipboard-change lines (it matches two specific patterns and discards everything else), but the permission itself is broad. Revoke it any time with `adb shell pm revoke com.walon.sharing android.permission.READ_LOGS`.
- **Display over other apps** is used only to show a 1-pixel invisible window for a moment so Android lets the app read the clipboard.
- While the mode is on, **everything you copy on the phone is sent to the paired laptop**, including passwords copied from a password manager. Turn the switch off, or use the one-tap button instead, if that is a concern.

## Audit log (plan 3, passes 1 and 2)

Each candidate from the first read was confirmed or rejected against the code, with a test where one is possible, and given a decision.

| # | Finding | Decision |
| --- | --- | --- |
| S1 | no `ReadHeaderTimeout`/idle timeouts; slowloris | **Fixed.** Header (which net/http also applies to the TLS handshake), keep-alive and per-request idle deadlines; tests against the real server configuration |
| S2 | no upload, entry or disk caps | **Fixed.** Limits above, 413/507, caps inside `Unpack`; tests for oversize, over-entry and simulated disk-full |
| S3 | `.partial-*` never swept | **Fixed.** Startup sweep that spares live sessions; janitor for sessions |
| S4 | no throttling or logging of auth failures | **Fixed.** Per-address failure counter, log line with address and path, no secrets |
| S5 | unvalidated peer name and device id | **Fixed.** 1-64 characters without control characters; `[A-Za-z0-9-]{8,64}`; CLI output also strips control characters |
| S6 | control socket under `/tmp`, chmod after listen | **Fixed.** Private directory, `umask 077`, ownership check, symlinked directory refused |
| S7 | websocket 32 KiB read limit vs 1 MiB clipboard | **Fixed and tested** with clips of exactly 1 MiB and 1 MiB + 1 (the oversized one is ignored, the connection stays up) |
| S8 | tokens never rotate; no last-seen | **Fixed.** `lastSeen` in `peers`, `sharingd rotate`. Expiry stays off by default |
| S9 | no confirmation on the laptop | **Fixed**, on by default (`--yes` to skip) |
| S10 | 30-year certificate, no rotation | **Accepted, documented.** Rotating means deleting the config directory and pairing again |
| S11 | exported share target forwards any URI | **Fixed.** `content://` only, own provider and private paths refused, 5-second Undo before anything is sent. Limitation: Android does not reliably tell the system share sheet from another app calling the activity, so the undo window applies to every share |
| S12 | token in plain `SharedPreferences`; backup | **Fixed.** AES-GCM under a non-exportable Keystore key (falls back to plain storage if the Keystore is broken, as on a few devices; plaintext from older versions is migrated and erased); `allowBackup=false`; the queue database and spool are in the no-backup directory |
| S13 | `READ_LOGS`/`SYSTEM_ALERT_WINDOW` in every build | **Fixed.** Opt-in build only; `scripts/check-manifest.sh` asserts it |
| S14 | `BootReceiver` exported | **Fixed:** `exported="false"`. Not yet confirmed on a real phone (system broadcasts do not need an exported receiver, but boot behaviour must be checked) |
| S15 | no stall timeout | **Fixed.** 30 s watchdog on every transfer and 30 s call timeout on control calls; tested against a server that goes silent |
| S16 | does the phone send the header before the pin check? | **Verified by design and by the TLS layer**: the pin check is part of the TLS handshake, which completes before any HTTP request is written. A direct test needs a second server with another certificate; not automated |

Findings of the second pass over the new surface:

- **Upload sessions** are owned by the peer that created them; another peer gets 404 for every method (tested for `HEAD`, `PATCH`, `DELETE` and folder completion). Session ids are 128-bit random.
- **Offers** are owned the same way; `offers.json` and the session sidecars are written with mode 0600.
- **Folder uploads** reuse `safeJoin` and reject control characters and duplicates; a folder is only published when the file count matches, as one rename.
- **Folder downloads** open each file through `os.Root`, so a directory swapped for a symlink after the manifest was frozen cannot lead outside the offered folder; a changed size or mtime answers 412.
- **Exhaustion:** unfinished uploads, concurrent transfers, open sessions and total declared bytes are all capped per peer.
- **Static analysis:** `golangci-lint` (govet, staticcheck, errcheck, ineffassign, unused) is clean. `gosec` raised 48 findings beyond unhandled `Close` errors, all reviewed: file and directory permission rules (received files are meant to be readable, so 0644/0755), integer-conversion warnings on free-space arithmetic, and taint warnings on paths that `safeJoin`/`SanitizeName`/`os.Root` validate are excluded in CI by rule id; the one real finding, a `Pack` that opened files by path after walking (symlink swap), now opens through `os.Root`. The legacy upload reply is served as `text/plain` with `nosniff`. `govulncheck` could not reach the vulnerability database from the build sandbox, so it only runs in CI.
- **Fuzzing:** `safeJoin`, `SanitizeName`, `Unpack`, the manifest parser and the websocket message decoder have fuzz targets (`go test -fuzz`); they ran clean for the budget used here. `Range` parsing is the standard library's `http.ServeContent`, covered by a table test instead.

## Hardening already in place

- Uploaded names are reduced to a single path element; tar extraction rejects absolute paths, `..`, symlinks, hardlinks and control characters, extracts to a staging directory first, and publishes nothing if it fails.
- Received files never overwrite existing ones (`name (1).ext`).
- Offers belong to one phone, survive restarts, and end on acknowledgement, cancellation, 30 minutes of inactivity after delivery, or 24 hours.
- Clipboard text over 1 MiB is ignored and never read in full from the clipboard tool.
- A second `sharingd` refuses to start rather than taking over the running daemon's control socket.

## Known limitations

- **Anyone on your LAN can reach port 53317.** They cannot do anything without a token, but they can see that the service exists (it is advertised over mDNS) and a client that fails authentication repeatedly shares an address-wide throttle with every other client behind the same NAT address. Do not use this on untrusted networks such as public WiFi.
- Content is protected in transit only. Received files are stored unencrypted in the receive folder, and unfinished uploads stay on disk until finished or expired.
- The Keystore wrapping does not help against a rooted phone, and the fallback for a broken Keystore stores the token in plain preferences.
- The 5-second Undo is the only confirmation for a share; it cannot distinguish the system share sheet from another app.
- Clipboard sync sends everything you copy on the laptop to paired phones (and, with the optional automatic mode, everything you copy on the phone to the laptop), including passwords if you copy them. Unpair or stop the service if that is a concern.
- If a certificate is ever lost or regenerated (deleting `~/.config/sharing`), phones must re-pair.
- Not verified on real hardware in this round: boot receiver with `exported="false"`, Keystore migration on an upgraded install, share-sheet spooling, the narrowed manifest permissions. See `docs/plan3.md` for the device matrix to run.

## Reporting a vulnerability

Please do not open a public issue for security problems. Use GitHub's private reporting: on the repository page choose **Security → Report a vulnerability**. Include what you found, how to reproduce it, and the version or commit. You can expect an acknowledgement within a week; fixes are released as soon as they are verified.
