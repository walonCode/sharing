# Plan 3: Resume, retries, and security audit

Depends on plans 1 and 2 (pairing, hub, transfers, clipboard, Android service). Goal: a transfer that is interrupted (WiFi drop, laptop sleep, app or daemon restart, phone reboot) continues where it stopped instead of being thrown away, every network call retries sensibly, and the whole system gets a security review with the findings fixed.

## Where we are today

- **Phone to laptop** (`PUT /upload`): the body streams into `.partial-*` and is deleted on any error (`SaveFile`, `Unpack`). A drop at 95% of a 4 GB video restarts from zero, and the Android side gives up after one attempt (`Connection.put` shows "Send failed").
- **Laptop to phone** (`GET /transfer/{id}`): `Registry.Take` deletes the offer on the first request, so a failed download cannot even be retried. No `Range` support, `Content-Length` only for files, folders are a live-streamed tar.
- **Sending when offline**: `Send` returns `ErrPeerOffline` (also when the hub buffer is merely full) and the transfer is lost.
- **Android**: websocket reconnect already has backoff (1s to 30s) and reacts to network changes, but transfers have no queue, no persistence, and `readTimeout(0)`/`writeTimeout(0)` means a silently dead WiFi link hangs a transfer until TCP gives up.
- **Security**: good base (pinned cert, hashed tokens, safe tar extraction, single-use offers), but the first read found gaps listed in step 8. They are candidates to confirm, not proven exploits.

## Design decisions

- **Resume is explicit and offset-based.** Uploads use a session (`POST /uploads`, `HEAD`/`PATCH /uploads/{id}`, tus-like); downloads use HTTP `Range`. The server is the source of truth for how many bytes it has durably stored.
- **Folders become manifests, not live tar streams.** At send time the sender freezes a sorted list of `{path, size, mtime}`; each file is then transferred (and resumed) on its own. This avoids a tar that breaks if a file changes mid-transfer and lets the phone write each file to MediaStore as it completes. The tar endpoints stay for one release for old clients.
- **Offers outlive the first request.** They are removed on an explicit `ack` from the phone (or idle expiry), and are persisted so a daemon restart does not lose them.
- **One retry policy everywhere**: exponential backoff with full jitter, reset on progress, retry only errors that can succeed later (table below). All mutating calls are idempotent.
- **Protocol version negotiation**: `hello` gains `proto: 2` and `caps`; the phone sends `X-Sharing-Proto: 2`. Either side falls back to the v1 endpoints when the peer is older.
- **Security fixes land before the new endpoints** (steps 8 and 9), so resumable uploads are built on top of limits, timeouts, and validation instead of inheriting their absence.

### Retry policy

| Outcome | Action |
| --- | --- |
| IO error, timeout, connection reset, stall (no bytes for 30s) | retry with backoff, resume from the server's offset |
| 408, 429, 502, 503, 504 | retry, honour `Retry-After` |
| 409 on `PATCH` (offset mismatch) | read the correct offset from the response, retry immediately |
| 412 / 416 on `Range` (file changed or range invalid) | restart that file from byte 0 once, then fail |
| 401 | stop, surface "pair again" (existing behaviour) |
| 403, 404, 410 | stop, drop the item (offer gone, expired, or revoked) |
| 413, 422, 507 | stop, permanent failure with the reason shown (too large, bad name, disk full) |

Backoff: base 1s, factor 2, cap 60s, full jitter. The attempt counter resets whenever the offset advances. Give up after 8 attempts without progress or 24h in the queue, whichever comes first. A network-available callback skips the wait.

## Step 8: Baseline and audit pass 1

1. Add CI (GitHub Actions): `go vet`, `go test -race`, `staticcheck`, `govulncheck`, `gosec`; mobile `bun run lint`, `bunx tsc --noEmit`, `bun test src`, plus a Kotlin compile of the module. Run `bun audit` (or osv-scanner against `bun.lock`) and Gradle dependency vulnerability checks.
2. Hygiene: 350 files under `mobile/modules/sharing-service/android/build/` are tracked in git. Ignore the folder, `git rm -r --cached` it, and check nothing in it contains machine-specific paths or secrets.
3. Write a short threat model into `Security.md` (assets: files, clipboard, device tokens, cert key; attackers: same-LAN stranger, malicious local app on laptop or phone, someone who saw the QR, lost or rooted phone, malicious paired peer) and review each of these against the code.
4. Triage the candidate findings below into fix-now / fix-later / accept-and-document, and record the decision per item in `Security.md`.

### Candidate findings from the first read (to confirm with tests)

| # | Area | Finding | Severity guess |
| --- | --- | --- | --- |
| S1 | Daemon | `http.Server` in `cmd/sharingd/main.go` has no `ReadHeaderTimeout`, `ReadTimeout`, or `IdleTimeout`, and `/pair` and TLS handshakes are reachable pre-auth: slowloris keeps connections open | medium |
| S2 | Daemon | Uploads have no size cap, no entry-count or total-size cap on tar extraction, and no free-disk check: a paired peer (or a buggy phone) can fill the disk | medium |
| S3 | Daemon | `.partial-*` files and staging dirs from a crash are never swept | low |
| S4 | Daemon | No rate limit or logging of failed auth and failed `/pair`. Token entropy makes guessing infeasible, but there is no signal if someone tries | low |
| S5 | Daemon | Peer `name` and `deviceId` from `/pair` are stored unvalidated (length, control characters): terminal escape injection in `peers`/logs, odd notification text | low |
| S6 | Daemon | `control.SocketPath` falls back to `os.TempDir()` when `XDG_RUNTIME_DIR` is unset, and the socket is `chmod`ed only after `Listen`: a local user can race or pre-create the path | low to medium |
| S7 | Daemon | `server.handleWS` uses `websocket.Accept` defaults; `coder/websocket` has a 32 KiB read limit, which is below the 1 MiB clipboard cap. Verify: oversized clips may close the connection instead of being ignored | low (reliability) |
| S8 | Daemon | Tokens never expire or rotate; no "last seen" to spot a stolen token | low |
| S9 | Daemon | Pairing trusts anyone who sees the QR within 5 minutes; no confirmation on the laptop | medium, by design |
| S10 | Daemon | Certificate is valid 30 years with no rotation story | low, document |
| S11 | Android | `ShareReceiverActivity` is exported and uploads whatever URI any app hands it, including `file://` and URIs into the app's own storage (its `SharedPreferences` holds the device token), with no confirmation | medium |
| S12 | Android | Device token in plain `SharedPreferences`; `allowBackup` not set in the manifest or `app.json`, so app data may be included in backups | medium |
| S13 | Android | `READ_LOGS` and `SYSTEM_ALERT_WINDOW` are declared in every build even though the automatic clipboard mode is opt-in | medium |
| S14 | Android | `BootReceiver` is `exported="true"`; system broadcasts do not need that. Check it can be `false` | low |
| S15 | Android | No stall timeout (`readTimeout(0)`) and no attempt limit: a half-dead link hangs a transfer indefinitely | reliability |
| S16 | Both | Confirm by test (not just by reading) that the phone sends no `Authorization` header before the pinned-cert check passes, and that a different cert at the same address fails closed | verify |

## Step 9: Daemon hardening (before any new endpoints)

Branch `fix/server-hardening`.

1. `http.Server`: `ReadHeaderTimeout` 10s, `IdleTimeout` 2m, `MaxHeaderBytes` 16 KiB. No global `ReadTimeout`/`WriteTimeout` (they would kill big transfers); instead wrap request bodies and response writers with an **idle deadline** that is extended on every read or write (`http.ResponseController.SetReadDeadline/SetWriteDeadline`), so a stalled peer is cut after 30s but a slow, steady one is fine. (S1)
2. Limits in `settings.json` with safe defaults: `maxUploadBytes` (default 64 GiB, 0 = disk only), `maxEntries` (200 000), per-peer concurrent transfers (4), minimum free space reserve (1 GiB). Check `Content-Length` up front (`413`), enforce while streaming with `http.MaxBytesReader`, check `statfs` (`507`). Apply the same caps inside `Unpack`. (S2)
3. Startup sweep of `.partial-*` older than the session TTL (step 11 replaces this for resumable sessions). (S3)
4. Auth middleware: constant-time compare stays; add a small per-IP failure counter (delay after 10 failures per minute), log failures with the peer IP but never the token or header, and the same for `/pair`. (S4)
5. Validate `name` (1 to 64 runes, no control characters, trimmed) and `deviceId` (`[A-Za-z0-9-]{8,64}`) on `/pair`; escape names when printing in the CLI. (S5)
6. Control socket: create under a `0700` directory (`$XDG_RUNTIME_DIR/sharing/`, or `~/.local/state/sharing/` if unset, never `/tmp`), set `umask 077` around `Listen`, then verify mode and owner. (S6)
7. `websocket.Accept` options: set `SetReadLimit(MaxClip + overhead)` after accept and check the clipboard path with a 1 MiB test; ignore unknown message types. (S7)
8. Device tokens: record `lastSeen` per peer and show it in `sharingd peers`; add `sharingd rotate <peer>` (new token pushed to the phone over the live websocket, old one valid for 60s). Token expiry stays off by default. (S8)
9. Pairing confirmation: `sharingd pair` waits and prints "Pixel 8 wants to pair, accept? [y/N]" before issuing the token, with an opt-out flag (`--yes`) for scripts. (S9)
10. Tests: slowloris against the real server config, oversize and over-entry uploads, disk-full simulation (injected `statfs`), auth-failure throttling, name validation table, socket permission and `/tmp` fallback tests, clip of exactly 1 MiB and 1 MiB + 1.

## Step 10: Retry foundation

Branch `feat/retry-policy`.

1. Go (`internal/retry`): `Backoff{Base, Cap, Jitter func}` and `Do(ctx, policy, fn)` with an error classifier matching the table above. Used by the CLI and any server-side outbound work. Injectable clock and jitter for tests.
2. Go: replace `ErrPeerOffline` for a full hub buffer with `ErrBusy`, and let `Hub.Send` report why it failed.
3. Kotlin: a `Retry` object with the same policy and classifier, pure Kotlin so it can be unit tested without Android. Add a JUnit test source set to the module `build.gradle` (the repo has none today); this is the one new piece of Kotlin test tooling.
4. Kotlin: stall watchdog. OkHttp `readTimeout`/`writeTimeout` stay unlimited, but each transfer wraps its streams in a counter and aborts the call if no bytes move for 30s. (S15)
5. Kotlin: persistent transfer queue, a small SQLite table in the module (no-backup directory): `id, direction, name, kind, source (uri/offer id), size, offset, state, attempts, nextRetryAt, remoteId (uploadId/offerId), error`. `SharingService` loads it on start (including after boot) and runs due items; `Connection.poke()` makes them due immediately when a network returns.
6. Tests: classifier table, backoff bounds with a fixed jitter source, attempt counter reset on progress, queue survives a simulated process restart.

## Step 11: Resumable uploads (phone to laptop)

Branch `feat/resumable-upload`.

Protocol (all authenticated, all idempotent):

```
POST  /uploads                {name, kind: file|file-in-dir, size?, dirId?, relPath?}
      -> 201 {id, offset: 0, maxChunk}
HEAD  /uploads/{id}           -> Upload-Offset: <bytes durably stored>, Upload-Length: <size or empty>
PATCH /uploads/{id}           Upload-Offset: N, [Upload-Complete: true], body = bytes from N
      -> 204 Upload-Offset: <new offset>          (409 + correct offset if N is wrong)
      -> 201 {name}                               (when the last byte arrived; publish done)
DELETE /uploads/{id}          -> 204              (abandon, removes the partial)
```

1. State: partial data in `<receive dir>/<peer folder>/.partial-<id>` with a sidecar `<config>/uploads/<id>.json` (peer id, name, size, created, last activity). `fsync` before acknowledging an offset so the reported offset is always safe.
2. One active `PATCH` per upload (second gets `423`). `PATCH` offset must equal the stored length, else `409`.
3. On completion verify length, then publish with the existing no-overwrite link (`publish`), and keep the result for 10 minutes so a repeated final `PATCH` or `HEAD` returns the same answer instead of creating a duplicate (`name (1).ext`) when only the response was lost.
4. Unknown size (some content URIs report none): accept `Upload-Complete: true` on the last `PATCH` instead.
5. Limits from step 9 apply per upload; a global cap on total partial bytes per peer; sessions idle for 72h are deleted by a janitor (also run at startup); unpairing deletes the peer's sessions.
6. Folders: `POST /uploads` with `kind: dir` returns a `dirId`; each file is its own upload carrying `dirId` and a sanitised `relPath`; `POST /dirs/{dirId}/complete {count}` publishes the folder atomically (staging directory, same rules as `Unpack`). Reuse `safeJoin`/`SanitizeName`; add the entry-count cap.
7. Keep `PUT /upload` for v1 phones, with the new limits and timeouts.
8. Kotlin client: each upload is a queue row. Start or resume with `HEAD` to learn the offset, then `PATCH` the rest from that offset in a streaming body (`InputStream` skip, or a seekable `ParcelFileDescriptor` when available). Update the persisted offset from responses, and obey the retry table.
9. Share-sheet URIs: the grant is temporary and usually dies with the process, so a retry after a restart can fail with `SecurityException`. For items under a size threshold (configurable, 256 MiB) copy to a no-backup spool directory when queued, and delete it on completion; for larger items retry only while the grant is alive and otherwise mark "Needs re-share" in the notification. Folders picked through SAF can use `takePersistableUriPermission`.
10. Tests (Go): resume after a connection killed mid-`PATCH` (hijack and close after N bytes), wrong offset, duplicate final `PATCH`, two concurrent `PATCH`es, partial cleanup after TTL, disk-full, peer unpaired mid-upload, folder atomic publish, v1 `PUT` still works. Race detector on.

## Step 12: Resumable downloads (laptop to phone)

Branch `feat/resumable-download`.

1. `Registry`: offers are no longer consumed by `GET`. States `pending`, `active`, `done`; removed on `ack`, on explicit `DELETE /transfer/{id}`, or after 30 minutes idle (absolute cap 24h). Ownership check stays. Persist to `<config>/offers.json` (atomic write) and reload on start; drop entries whose file vanished.
2. Offer records `size`, `mtime`, and an `ETag` (`"<size>-<mtimeNs>"`) frozen at send time.
3. Files: serve with `http.ServeContent` semantics: `Range`, `If-Range`, `206`, `Accept-Ranges`, correct `Content-Length`. If the file changed since the offer, answer `412` and the phone restarts it from byte 0 and reports it.
4. Folders (proto 2): `GET /transfer/{id}/manifest` returns the frozen list; `GET /transfer/{id}/files/{n}` serves entry `n` with `Range`. Files are opened with `O_NOFOLLOW` and revalidated against the manifest (size and mtime). Old phones keep the tar endpoint.
5. Integrity: `GET /transfer/{id}/sha256` (files and manifest entries) computed in the background after the offer is sent; the phone verifies after the last byte when it is available and re-downloads once on mismatch. This catches corruption that a resumed transfer can cause.
6. Protocol: add `ack {id, status: ok|failed|cancelled}` (phone to laptop), handled by the daemon to finish the offer and report back to the CLI.
7. Kotlin: a download queue row stores the offer id, the MediaStore URI of the **pending** entry, and the offset. Resume by opening the output stream in append mode (`"wa"`) and sending `Range: bytes=<offset>-`; verify the on-disk length matches the stored offset first and truncate if not. Set `DATE_EXPIRES` on pending entries so abandoned ones clean themselves up. Folders write each file as it completes and publish nothing partial; on failure of the whole transfer delete only the files this transfer created.
8. Tests (Go): `Range` table (start, middle, suffix, past end, `If-Range` hit and miss), offer survives a failed GET, offer removed on `ack`, daemon restart reloads offers, file modified between offer and download, folder manifest with a concurrent modification, ownership across two peers, expiry.

## Step 13: Outbox, progress, and CLI

Branch `feat/outbox`.

1. `sharingd send` no longer fails when the phone is offline: the offer is queued (persisted with the offers) and delivered when the phone's websocket connects (`hello` replays pending offers). Flag `--wait` blocks and shows progress; default returns immediately with "queued".
2. `sharingd transfers` lists active and queued transfers in both directions with progress, speed, attempt count, and last error; `sharingd cancel <id>` and `sharingd retry <id>`.
3. Protocol: optional `progress {id, offset}` from the phone every few seconds for downloads; the daemon knows uploads from its own offsets. Control API exposes both.
4. Android notification shows progress for the active transfer with a Cancel action, and a failure notification offers Retry (re-queues immediately).
5. Clipboard: if a phone-to-laptop clip fails to send (`ws.send` returns `false` today and is ignored), keep the latest value and resend after reconnect, newest only.
6. Pairing retry: if the `/pair` response is lost, a repeat of the same invite from the same `deviceId` within 2 minutes issues a fresh token instead of failing with 403 (the invite token is the credential either way, and the old token is replaced).

## Step 14: Android hardening

Branch `fix/android-hardening`.

1. Share target: accept only `content://` URIs; reject `file://` and any URI under the app's own data directories or provider; show a short "Send N file(s) to <laptop>?" confirmation as a notification action when the sender is not the system share sheet (use `getReferrer`/caller checks as far as Android allows), and add an "Undo" for the first seconds. (S11)
2. Token storage: wrap the device token with an `AndroidKeyStore` AES-GCM key (non-exportable) and keep only ciphertext in `SharedPreferences`; migrate the plain token on first run. (S12)
3. `allowBackup="false"` (and `dataExtractionRules`/`fullBackupContent` excluding `sharing` prefs and the queue) via `app.json`/the module manifest, whichever prebuild honours; verify in the merged manifest. (S12)
4. `READ_LOGS` and `SYSTEM_ALERT_WINDOW`: move to an opt-in build (separate manifest overlay selected by a Gradle property, e.g. `make apk` vs `make apk-auto-clipboard`) so the default APK does not request them at all. (S13) Open decision below.
5. `BootReceiver`: set `exported="false"` if boot still works on the test phone. (S14)
6. Storage: cap entries and total size when extracting a laptop tar and reject control characters in names, as on the daemon.
7. Pairing screen: show the laptop name and fingerprint prefix for visual confirmation; never log invite URLs or tokens (`adb logcat` check).
8. Tests: compile the module, unit tests for URI filtering and the keystore wrapper (instrumented where needed), and the manifest assertions (script that greps the merged manifest for `READ_LOGS`, `allowBackup`, exported flags).

## Step 15: Audit pass 2, docs, and device test

Branch `docs/security-and-resume`.

1. Review all code added in steps 9 to 14 against the threat model; specifically the new surface: upload sessions (ownership, IDOR on `{id}`, resource exhaustion), manifest paths, offer persistence file permissions (`0600`), ack/cancel races.
2. Fuzz targets (`go test -fuzz`) for `safeJoin`, `SanitizeName`, `Unpack`, the manifest parser, the websocket message decoder, and `Range` parsing; run for a fixed budget in CI and keep the corpus.
3. Re-run `gosec`, `govulncheck`, `bun audit`, Android lint; zero new high findings.
4. Update `Security.md` (new limits, the remaining known limitations, the rotation and confirmation features), the README (resume and `transfers` commands, how to cancel and retry), and mark the plan's implementation notes at the bottom of this file.
5. Real device test (ask first, per the phone testing etiquette: do not tap or screenshot the phone unprompted). Matrix: airplane mode mid-upload and mid-download; kill the app; reboot the phone mid-transfer; `systemctl --user restart sharingd` mid-transfer; laptop suspend and resume; switch WiFi networks and change the laptop IP; 5 GB file; folder with 10 000 files; low phone storage; low laptop disk; unpair during a transfer; share a file, kill the app, check the retry outcome.

## Done criteria

`go vet ./... && go test -race ./...` clean, fuzz targets run clean for their budget; mobile lint, typecheck, `bun test src`, and the Kotlin module compile clean; the device matrix above passes on the real phone; `Security.md` lists every audit item as fixed, accepted (with reason), or deferred; no tracked build output; all dev services, daemons, and emulators stopped afterwards.

## Branches/PRs (kept after merge)

`chore/ci-and-audit-baseline`, `fix/server-hardening`, `feat/retry-policy`, `feat/resumable-upload`, `feat/resumable-download`, `feat/outbox`, `fix/android-hardening`, `docs/security-and-resume`, each merged into `main` via PR.

## Open decisions (recommendation first)

1. **Folder transfer format.** Recommended: per-file manifest in both directions, tar kept only for old clients. Alternative: keep tar and resume by byte offset over a frozen snapshot (smaller change, but breaks if any file changes size mid-transfer).
2. **Default APK and `READ_LOGS`.** Recommended: default build does not declare `READ_LOGS` or the overlay permission; a second build target enables automatic clipboard. Alternative: keep declaring them and document it (current behaviour).
3. **Pairing confirmation on the laptop** on by default (recommended, `--yes` to skip) versus opt-in.
4. **Spool size for share-sheet files** (256 MiB suggested): larger means more phone storage used while queued, smaller means more "needs re-share" failures after a restart.
5. **Kotlin unit-test tooling** (JUnit source set in the module): recommended, since retry and queue logic is the part most likely to regress and cannot be covered from the TypeScript side.


## Implementation notes

Written after the work was done. What changed, what was measured, and what was not possible.

### Done

- **Step 8 (baseline and audit).** `.github/workflows/ci.yml` runs gofmt, vet, `go test -race -cover`, golangci-lint, staticcheck, govulncheck, gosec, the fuzz targets for a fixed budget, the mobile checks (tsc, lint, prettier, `bun test`, `bun audit`) and an Android job (ktlint, Kotlin compile, JVM unit tests including the daemon interop test, Android lint, merged-manifest assertions for the default and the opt-in build). 350 tracked Gradle outputs were removed from git and the folder is ignored. The threat model and the decision per finding S1 to S16 are in `Security.md`.
- **Step 9 (daemon hardening).** All items, with the settings keys and defaults listed. The first design added a `ConnState` hook for the TLS handshake, on the assumption that `ReadHeaderTimeout` does not cover it; a probe showed that net/http already applies the smallest of `ReadHeaderTimeout`, `ReadTimeout` and `WriteTimeout` to the handshake, so the hook was removed. Tests connect and stay silent, stall the headers, stall a body, and hold a keep-alive connection past the timeout. Pairing confirmation is on by default.
- **Step 10 (retry foundation).** `internal/retry` (Go) and `Retry.kt` implement the same table; both have the classifier, backoff bounds, attempt reset on progress, give-up and Retry-After tests. One addition to the table: an unreachable laptop (no connection made) does not spend attempts, because eight attempts would give up after four minutes while the laptop is merely off. `Hub.Send` reports `ErrOffline` or `ErrBusy`. The stall watchdog and the persistent queue are in the Kotlin module (`StallGuard`, `SqliteTransferStore`, `Transfers`); the queue survives a simulated process death in `TransfersTest`.
- **Step 11 (resumable upload).** The protocol in the plan, with 423 for a concurrent writer after a 2 s wait (a client that retries right after a dropped connection otherwise races the handler still noticing it), 409 with the stored offset, 10-minute result retention, folder sessions published by one rename, and the v1 `PUT` kept. The Kotlin client resumes from the laptop's offset and falls back to the v1 `PUT` when `/uploads` answers 404/405/501. Small share-sheet files are spooled (`Spool`); larger ones are retried only while the grant lasts.
- **Step 12 (resumable download).** Persisted offers with states, `ETag` and frozen size and mtime, `http.ServeContent` ranges and `If-Range`, 412 on change, folder manifests with `os.Root` opens, background SHA-256, `ack`/`progress`/`cancel` messages. An HTTP `POST /transfer/{id}/ack` was added because a websocket that is down at the end of a download would otherwise lose the result. The phone writes to a pending MediaStore entry with `DATE_EXPIRES`, checks the entry's real length against the stored offset on every attempt, and verifies the checksum when the laptop has one (one automatic re-download on mismatch).
- **Step 13 (outbox, progress, CLI).** Offline sends are queued and replayed on connect; `sharingd transfers|cancel|retry`, `send --wait`; progress and Cancel/Retry in the notification, one summary per direction instead of a notification per file; the newest unsent clipboard value is resent after reconnect; a lost `/pair` response can be repeated by the same device for 2 minutes.
- **Step 14 (Android hardening).** URI policy and 5 s Undo, Keystore-wrapped token with migration, `allowBackup=false`, the opt-in build for `READ_LOGS`/overlay (via `app.config.ts`, decision 2 below), `BootReceiver` not exported, tar caps and control-character checks, fingerprint review on the pairing screen, manifest assertion script.
- **Step 15 (audit pass 2, docs).** Second pass recorded in `Security.md`; fuzz targets for `safeJoin`, `SanitizeName`, `Unpack`, the manifest parser and the message decoder (each ran clean for 5 to 9 s locally, 15 s per target in CI); README, Contributing and CLAUDE.md updated.

### Decisions taken (the plan's open decisions, recommendation first)

1. Folder format: per-file manifests both ways; tar kept for old peers.
2. `READ_LOGS` and the overlay permission only in the opt-in build (`make apk-auto-clipboard`).
3. Pairing confirmation on by default, `--yes` to skip.
4. Spool threshold 256 MiB, adjustable in `Prefs`.
5. Kotlin test tooling: JUnit plus MockWebServer in the module, and an interop test that runs a real daemon.

### Verification actually performed

- Go: `go vet`, `go test -race ./...`, golangci-lint (0 issues), gosec with the documented exclusions (0 issues), fuzz targets. `govulncheck` could not reach its database from the sandbox.
- Kotlin: the module compiled with kotlinc against the Android 14 framework jar (the Expo bridge file excluded), 64 JVM tests passed, including 8 interop tests against the real `sharingd` (resume both ways after a killed source or sink, folder upload and download, vanished session, changed file, ack, stall guard).
- Mobile: tsc, `expo lint`, prettier, `bun test`, a Metro bundle for Android, `expo prebuild` (the generated manifest has `allowBackup=false`, no log or overlay permission, no storage permissions).

### Not done, and why

- **The device matrix of step 15.5** needs a phone (and the plan says to ask before touching it). Nothing here ran on hardware. Items most likely to need attention: boot receiver with `exported="false"`, Keystore wrapping on the user's Samsung, MediaStore pending entries with `DATE_EXPIRES` and `wa` append, share-sheet spooling, notification actions on Android 14+.
- **Gradle build of the module and the merged-manifest check** could not run (no Android SDK in the sandbox); CI runs them. `lint-baseline.xml` is empty because Android lint was never run; with `abortOnError true` the first CI run may report existing issues, which should be recorded with `./gradlew :sharing-service:updateLintBaseline`.
- **Real distinction between the system share sheet and other callers** is not possible on Android; the Undo window applies to every share.
- **Branches and pull requests.** The plan lists eight branches; this work was committed on the single branch this session was given, in one commit per theme.
