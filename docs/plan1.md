# Plan 1: Go daemon core (pairing/protocol, discovery, transfer)

Scope: everything in `api/` needed for a paired phone to reach the laptop and move files and folders. Plan 2 covers clipboard, packaging, and Android.

## Architecture decisions

- **The laptop is the only server.** The phone is always a client. This keeps the Android side simple (no listening TLS server) and matches "the service announces, the phone picks it up".
- **Transport:** HTTPS with a self-signed ECDSA certificate generated on first run. The phone pins the certificate's SHA-256 fingerprint, which it learns during pairing. No CA or system trust involved.
- **Auth:** pairing exchanges a one-time invite token for a long-term per-peer token. Every later request carries `Authorization: Bearer <peerToken>`. The daemon stores only the SHA-256 of each peer token.
- **Laptop to phone transfers:** the laptop sends an `offer` message over the persistent websocket; the phone then downloads it with `GET /transfer/{id}`.
- **Phone to laptop transfers:** `PUT /upload?name=...&kind=file|dir`. Folders are streamed as tar and extracted safely (no path traversal, never overwrite: rename on collision).
- **Control:** the CLI talks to the daemon over a unix socket in `$XDG_RUNTIME_DIR`, never over the network.

## Layout

```
api/cmd/sharingd/main.go          daemon + CLI entry (subcommands: run, pair, send, peers)
api/internal/config/              config dir, settings, peer store, TLS cert
api/internal/pairing/             invite creation/redemption, token hashing
api/internal/protocol/            ws message types (hello, offer, ack, later: clip)
api/internal/server/              HTTPS routes, auth middleware, ws hub
api/internal/discovery/           mDNS advertise (browse helper for tests)
api/internal/transfer/            tar pack/unpack, safe paths, offer registry
```

## Step 1: Pairing and protocol

1. `config`: load/save `~/.config/sharing/` (settings.json: device id, name, receive dir, port; peers.json; cert.pem/key.pem).
2. `pairing`: `NewInvite` returns token (32 random bytes, base64url), TTL 5 minutes, single use. Invite URL: `sharing://pair?host=..&port=..&fp=<cert sha256>&token=..&name=..` (shown as text and QR in the terminal).
3. `server`: `POST /pair {token, deviceId, name}` returns `{peerToken, laptopName}`; stores the peer. Bearer middleware for everything else. `GET /ws` upgrades and registers the connection with the hub; first message from the server is `hello`.
4. Tests: invite expiry and single use, bad token rejected, unauthenticated ws rejected, hub delivers to the right peer.

## Step 2: Discovery

1. Advertise `_sharing._tcp` via mDNS with TXT records `id`, `name`, `fp` (fingerprint prefix for sanity check), on the configured port. Re-announce on network changes (interface watcher) so IPs can change freely.
2. Phone browses the same service type and verifies the full pinned fingerprint on connect.
3. Tests: advertise then browse on loopback, TXT contents, shutdown de-registers.

## Step 3: File and folder transfer

1. `transfer`: stream tar writer/reader; sanitize entry names; reject absolute paths, `..`, symlinks escaping the root; unique-name on collision.
2. Upload route writes to the receive dir through a temp file then atomic rename. Download route serves registered offers (one-shot, expires).
3. CLI: `sharingd send <path> [--to <peer>]` registers an offer and pushes `offer` over the peer's websocket. `peers` lists paired devices.
4. Tests: round-trip file and nested folder, traversal attempt, collision rename, large file streaming, offer expiry.

## Done criteria

`go vet ./... && go test ./...` clean; a manual `curl` pairing + upload against a running daemon works; daemon stopped afterwards.

## Branches/PRs (kept after merge)

`feat/pairing-protocol`, `feat/discovery`, `feat/transfer`, each merged into `main` via PR.
