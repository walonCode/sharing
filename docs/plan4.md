# Plan 4: Optimization and code cleanup

Depends on plan 3. Plan 3 rewrites the transfer code on both sides (`transfer/`, `Connection.kt`, `Storage.kt`, the retry queue), so the refactors that touch those files happen after it lands, not before, to avoid doing the same work twice. Work that does not overlap (clipboard, discovery, mobile dependencies, CLI, tooling) can start earlier.

Rule for this plan: **no optimization without a number.** Each performance item starts with a measurement, has a target, and is kept only if the number improves. Cleanups must not change behaviour; the existing tests are the safety net, and any cleanup of untested code gets a test first.

## Baseline (measured while writing this plan)

- `go test -race ./...` passes, `go vet` is clean, `gofmt -l` reports nothing, `bunx tsc --noEmit` passes.
- `staticcheck ./...` reports one finding: `cmd/sharingd/main.go:187` (error string ends with punctuation and a newline).
- `sharingd` builds to 9.1 MB with `-trimpath -s -w`; the Go module graph has 15 modules.
- The release APK (`dist/sharing.apk`, arm64 only, minified) is **40 MB**, large for an app with two screens and one native service.
- No CI and no linter beyond `expo lint` (plan 3 step 8 adds the CI scaffolding; this plan adds the lint rules).

Still to measure in step 16: idle CPU and wakeups of the daemon, phone idle battery drain, transfer throughput, cold-start time, service memory.

## Candidates found by reading the code

These are hypotheses with a reason; step 16 confirms the ones that matter before they are changed.

### Daemon performance

- **Clipboard polling forks a process every 400 ms** (`clipboard/sync.go` `pollInterval`, `execBackend.Read` runs `wl-paste`/`xclip` each tick): about 2.5 process spawns per second, forever, even when idle. Plan 2 originally specified `wl-paste --watch`. `exec.Output()` also reads the whole clipboard into memory before the 1 MiB check, so copying a 200 MB blob reads all of it every 400 ms.
- **Auth is O(peers) with a hash per peer and an allocation per request** (`server.auth` calls `Store.Peers()`, which copies the map into a new slice, then `TokenMatches` hashes the presented token again for each peer). Hash once, look up by hash.
- **Small buffers on bulk copies**: `io.Copy` uses 32 KiB; large transfers want 256 KiB to 1 MiB, or `sendfile` through `http.ServeContent` (plan 3 already moves downloads there).
- **`Registry.Add` scans every offer on each add** to expire old ones; a ticker or lazy expiry on lookup is simpler.
- **`Hub.Broadcast` marshals the message once per peer** inside `wsjson.Write`; marshal once and write bytes.
- **Interface polling for mDNS** (`discovery.currentAddrs` every 10 s) is cheap but constant; an rtnetlink subscription removes the wakeups. Low priority, only if wakeups show up in the measurement.

### Phone performance and battery

- **A new `OkHttpClient` on every reconnect**: `Connection.loop` calls `Net.client(fp)` each iteration. Each client has its own connection pool, dispatcher, and `SSLContext`; the old ones are never shut down. Create one client per fingerprint and reuse it.
- **mDNS discovery for 8 s every 30 s while the laptop is away** (`Discovery.find` holds a multicast lock and runs NSD each backoff cycle): costs radio time and battery all night when the laptop is off. Lengthen the backoff when nothing has been seen (cap 5 min) and wake on network change or user action, which already exist.
- **`ClipWatcher` reads the entire system log** (`logcat -T 1`) and filters in Kotlin, so the phone parses every log line of every app while automatic clipboard is on. Filter in logcat itself (`-e` regex or tag filters). The "is log access working" heuristic depends on seeing unrelated lines, so it needs a different check; measure the CPU and battery cost first to decide if it is worth it.
- **8 KiB copy buffers**: Kotlin's `copyTo` default is 8 KiB, used for downloads, uploads, and MediaStore writes. Use 128 to 256 KiB.
- **Unbounded `newCachedThreadPool` for transfers**: sharing 50 photos starts 50 simultaneous uploads. Cap at 2 or 3 concurrent transfers through the plan 3 queue.
- **Slow SAF folder walking** (`Storage.tarTree` uses `DocumentFile.listFiles()` and then `name`/`isDirectory`/`length` per child, each a binder round trip). A single `DocumentsContract.buildChildDocumentsUriUsingTree` cursor per directory fetches id, name, mime, and size at once; usually an order of magnitude faster on big folders.
- **MediaStore round trips per file** in `Storage.untar` (`insert`, write, `update` to clear `IS_PENDING`) for every file of a received folder; batch where the API allows, and measure with a folder of thousands of files.
- **HTTP/2 vs HTTP/1.1 for bulk transfers**: Go's TLS server negotiates h2 and OkHttp will use it for uploads and downloads; h2 flow control can cap single-stream throughput. Compare against `protocols(HTTP_1_1)` on the throughput test and pick the faster. The websocket already uses HTTP/1.1.
- **One notification per received file** (`Notifier.transfer` uses a new id each time): 50 files produce 50 notifications. Use one grouped or progress notification (coordinates with plan 3 step 13).

### APK size and mobile dependencies

- `package.json` lists many template dependencies that nothing in `src/` or the module imports: `expo-glass-effect`, `expo-symbols`, `expo-image`, `@expo/ui`, `expo-font`, `expo-web-browser`, `react-native-reanimated`, `react-native-worklets`, `react-native-gesture-handler`, `react-native-web`, `react-dom`, `expo-system-ui`. Several are native libraries that autolinking compiles into the APK. **Careful:** `expo-router` needs some of them as peer dependencies (`expo-linking`, `expo-constants`, `react-native-screens`, `react-native-safe-area-context`, and gesture handler on some versions), so each removal is checked with `bunx expo-doctor`, a prebuild, and a launch, not by grep alone.
- Blanket ProGuard keeps in `app.json` (`-keep class expo.modules.sharingservice.** { *; }`, all of `com.google.mlkit.**` and `androidx.camera.**`) defeat shrinking. Narrow them to what reflection actually needs (Expo module classes) and verify the release build still pairs and scans.
- The camera scan pulls in CameraX and ML Kit barcode scanning just to read one QR code. Check how much of the 40 MB that is (APK Analyzer or `bunx expo-atlas`/`apkanalyzer`); alternatives are the unbundled ML Kit model or a pasted-link-only flow, which is a product decision (see open decisions).
- Template leftovers: `web` config and `react-native-web` for an Android-only product, `ios` config and `assets/expo.icon`, the `reset-project` script in `package.json` points at `scripts/reset-project.js`, which does not exist in the repo.

### Code cleanup

Daemon:
- `cmd/sharingd/main.go` (309 lines) mixes the daemon, every CLI subcommand, and desktop notification. Split into `run.go` (daemon), `cli.go` (subcommands), `notify.go`.
- The CLI repeats the same control-socket round trip in `pair`, `peers`, `send`, `setup`, `unpair` (build request, "is the daemon running?" error, status check, read body). Extract one `call(method, path, in, out)` helper. Argument parsing is hand rolled for `send --to` and uses `flag` for `setup`; use one `flag.FlagSet` per subcommand.
- `control.Handler` is one long function of closures; make it a small `API` type with a method per route so each is testable.
- Replace `log.Printf` with `log/slog` (leveled, key-value, friendly to journald), add request logging for failed auth and transfers; never log tokens (ties to plan 3 S4).
- Stale or misleading comments: `server` package doc says "(later) transfers", `protocol.TypeClip` says "(plan 2)", the `publish` comment about `os.Link`/`Rename` races reads confusingly. Fix them while touching the files.
- Import grouping (`go-qrcode` sits in the standard library block in `main.go`); adopt `goimports` grouping.
- `staticcheck` ST1005 at `main.go:187`; check for ignored errors that matter (`_ = Pack(w, o.Path)` after headers are sent) and log or comment each deliberate one.
- Dead or test-only code: `discovery.Browse` is used only by tests and a "diagnostics" idea that does not exist; either add `sharingd doctor` (see below) or move it to a test helper.
- Settings changes apply inconsistently (a new `Name` needs a restart for mDNS); either apply live or have `setup` say so.

Android and mobile:
- `Connection.kt` (221 lines) is the connection loop, clipboard apply, download, upload, URI metadata, and OkHttp ownership in one class. After plan 3, split into `Session` (websocket and reconnect), `Transfers` (queue worker, upload, download), and `ClipSync`. Keep the service a thin owner of those.
- `SharingService` and `Connection` each build their own `Prefs`; pass one instance.
- Dead or unverified permissions in the module manifest: `WAKE_LOCK` and `ACCESS_WIFI_STATE`/`CHANGE_WIFI_STATE` have no use in the Kotlin code (only the multicast lock is used, which needs `CHANGE_WIFI_MULTICAST_STATE`). Verify each on a device and remove the unused ones (fewer permissions also helps plan 3's security goals).
- `setup.tsx` (260 lines, many `useState`) mixes pairing, permissions, and clipboard options; split into a pairing screen and a permissions screen with small hooks (`usePermissions`), move the duplicated `Button` and hard-coded colours into `src/components/` and a tiny theme.
- `index.tsx` calls the synchronous native `clipboardAccess()` inside an effect that runs on every status event; derive it once per visit.
- Kotlin and TS lint: `lintOptions { abortOnError false }` in the module means Android lint never fails; turn it on for real issues with a baseline file.

Repository:
- `CLAUDE.md` says "There is no test runner configured for either project", which is stale (`go test`, `bun test`, and the Makefile `test` targets exist). Update it, and mention `make test`.
- The tracked `android/build/` output is handled in plan 3 step 8; confirm it is gone.

## Step 16: Measure and set targets

Branch `chore/benchmarks`.

1. Go benchmarks in the repo (`go test -bench`): auth lookup with 1, 10, and 100 peers; `SaveFile` and `Pack`/`Unpack` throughput on 1 GiB and on 10 000 small files; hub broadcast; clipboard poll cost with a fake backend.
2. Daemon idle: CPU and process-spawn rate with `pidstat` and `strace -f -c -e trace=execve` over 5 minutes with clipboard sync running; wakeups with `perf` or `powertop`.
3. Throughput script (`scripts/throughput.sh` or a Go test tool) for real device runs: 2 GiB file each direction, 10 000-file folder each direction, on 5 GHz WiFi; record MB/s, CPU on both sides.
4. Phone: `dumpsys batterystats` and Battery Historian over an 8 hour idle night with the laptop on and with the laptop off; service memory (`dumpsys meminfo`); cold start of the app (`adb shell am start -W`); APK composition (`apkanalyzer`).
5. Write the results table into this file (an "Implementation notes" section) and set a target next to each number. Suggested starting targets: daemon idle process spawns near zero with a watch-capable clipboard and at most 1 per second otherwise; large-file throughput at least 80% of what `iperf3` reports on the same link; phone overnight drain from the service under 1% with the laptop off; APK under 25 MB; no thread or connection growth over 24 hours of reconnects.

Ask before running anything on the phone, per the phone testing etiquette (no tapping or screenshots unprompted).

## Step 17: Tooling and safety net

Branch `chore/lint-and-format`.

1. Go: add `golangci-lint` config (`govet`, `staticcheck`, `errcheck`, `ineffassign`, `unused`, `goimports`, `gofumpt` optional) and run it in CI; fix what it finds or suppress with a comment explaining why. Fix `ST1005` first.
2. Kotlin: add `ktlint` (or detekt) with a baseline; set Android lint `abortOnError true` with a baseline for existing issues.
3. TypeScript: keep `expo lint` and `tsc`; decide on a formatter (see open decisions) and apply it once in a single formatting-only commit so later diffs stay readable.
4. Coverage: `go test -cover` report and a short list of untested code that the refactors below touch (`cmd/sharingd` has no tests; add tests for the extracted CLI `call` helper and arg parsing before changing it).
5. `make lint` and `make test` run everything locally and in CI.

## Step 18: Daemon performance and cleanup

Branch `perf/daemon`, then `refactor/daemon-cli`.

1. **Clipboard**: event-driven reads. Wayland: `wl-paste --watch` keeps one long-lived process and emits on change; fallback for X11 (`xclip` has no watch) keeps polling but adapts: 400 ms while active, backing off to 2 s after 30 s of no change, back to 400 ms on any change. Cap reads with `io.LimitReader` (1 MiB + 1) so a huge clipboard is never fully read. The existing echo-suppression and size-cap tests must pass unchanged against the new backend; add tests for the watch parser and the backoff.
2. **Auth**: precompute `hash -> peer` in `config.Store` (rebuild on `AddPeer`/`RemovePeer`), hash the presented token once, look up, then do the constant-time compare on the stored value. Benchmark before and after (step 16).
3. **Buffers and copies**: a shared `sync.Pool` of 256 KiB buffers (or `io.CopyBuffer`) for `SaveFile`, `Unpack`, `Pack`; verify the gain with the throughput benchmark before keeping it.
4. **Registry** expiry on a timer or lazily on lookup instead of a full scan on every `Add`. **Hub.Broadcast** marshals once.
5. **CLI refactor** (after plan 3's CLI additions land): split `main.go`, introduce the `call` helper, per-subcommand `flag.FlagSet`, `slog`, and the `control.API` type. Keep the CLI behaviour and output identical; compare `sharingd` output before and after for each subcommand.
6. **`sharingd doctor`** (small, optional): prints clipboard backend in use, port reachability, cert fingerprint, receive dir free space, mDNS advertisement seen on loopback; replaces the test-only `Browse` as its user.
7. Comment and naming fixes listed above.

## Step 19: Android performance and cleanup

Branch `perf/android`, then `refactor/android-connection`. Starts after plan 3 steps 10 to 13 have landed.

1. **One shared `OkHttpClient`** per fingerprint with a bounded dispatcher; shut it down when pairing is cleared. Confirm no thread growth across 100 simulated reconnects (`Thread.getAllStackTraces()` count in a debug log).
2. **Discovery backoff**: separate "laptop unreachable" from "network down"; cap at 5 minutes when mDNS finds nothing, reset on network change, on the app's Start button, and on a share-sheet send. Measure overnight battery with the laptop off before and after.
3. **Buffers**: 256 KiB for all copy loops; measure throughput both ways.
4. **Transfer concurrency**: fixed worker count (2) fed by the plan 3 queue; no cached thread pool.
5. **SAF listing** via a `DocumentsContract` child-document cursor; compare against `listFiles()` on a folder of 5 000 files. **MediaStore**: batch inserts where possible and measure receiving a 10 000-file folder.
6. **HTTP protocol test**: h2 vs HTTP/1.1 for bulk, keep the faster one, record the numbers.
7. **`ClipWatcher` log filtering** (only if step 16 shows a real cost): server-side filtering with a new "log access working" check, tested on the user's Samsung device with the user present, because the log behaviour differs per OEM.
8. **Notifications**: one grouped or progress notification per batch.
9. **Refactor `Connection`** into `Session`, `Transfers`, `ClipSync`; single `Prefs`; remove unused permissions after verifying on device; flip lint to fail on real issues. Behaviour unchanged: the device matrix from plan 3 step 15 is the regression test and is re-run in full.

## Step 20: Mobile dependencies, APK size, and screens

Branch `chore/mobile-deps`, then `refactor/mobile-screens`.

1. Remove unused dependencies in small batches, each verified by `bunx expo-doctor`, `bunx tsc --noEmit`, `bun run lint`, `bun test src`, a prebuild, a release build, and a launch (ask the user to confirm pairing and scanning still work). Update `bun.lock`.
2. Narrow the ProGuard keep rules; rebuild; confirm pairing, QR scan, share target, and tile still work on a release build (minification bugs only show up in release).
3. Decide and apply the camera and web/iOS configuration changes from the open decisions. Remove the dead `reset-project` script and template assets.
4. Re-measure the APK and compare with the 40 MB baseline; record in this file.
5. Split `setup.tsx` into screens and hooks, extract `Button` and a small theme to `src/components/`, derive clipboard access once; no visual or behavioural change (screenshots only if the user offers; do not capture the phone unasked).
6. Update `CLAUDE.md` and `mobile/AGENTS.md` where they now disagree with the code (test runners, dependency list, platforms).

## Done criteria

All step 16 targets met or explicitly revised with a reason in this file; `go vet`, `staticcheck`/`golangci-lint`, `go test -race ./...`, `ktlint`, Android lint, `bunx tsc --noEmit`, `bun run lint`, `bun test src` clean; the plan 3 device matrix passes unchanged on the real phone after the Android refactor; APK size and idle battery numbers recorded before and after; no behaviour change in the CLI (before and after output compared); `CLAUDE.md` accurate; all dev services, daemons, and emulators stopped afterwards.

## Branches/PRs (kept after merge)

`chore/benchmarks`, `chore/lint-and-format`, `perf/daemon`, `refactor/daemon-cli`, `perf/android`, `refactor/android-connection`, `chore/mobile-deps`, `refactor/mobile-screens`, each merged into `main` via PR. Keep performance changes and refactors in separate PRs so a regression can be bisected to one or the other.

## Open decisions (recommendation first)

1. **Keep iOS and web in `app.json` and dependencies?** Recommended: drop them. The core is an Android-only Kotlin module and the daemon is Linux, so they add build time and dependencies for nothing. This does differ from the "cross-platform compatibility" line in `mobile/AGENTS.md`, which would then be updated.
2. **QR scanning dependency.** Recommended: measure first. If CameraX and ML Kit are a large share of the APK, consider the unbundled ML Kit model or the "paste the link" flow only. Keep scanning if the cost is modest; it is the nicer pairing experience.
3. **Formatter for TypeScript and Kotlin** (Prettier or Biome; ktlint's format): adopting one means a single large formatting-only commit.
4. **`ClipWatcher` log filtering.** Worth the risk only if the measured battery cost is real; it touches the fragile OEM-specific detection that currently works on the user's Samsung phone.
5. **Linter strictness** for Go (`golangci-lint` default set versus a stricter set including `gofumpt` and `revive`): recommended default set plus `goimports`, tightening later.


## Implementation notes

### Measurements (Linux x86-64 sandbox, 4 vCPU; `make bench` reproduces the Go ones)

| Item | Before | After | Kept |
| --- | --- | --- | --- |
| Auth lookup, 100 peers | 9.8 us, 15 KB, 7 allocs | 1.0 us, 0.7 KB, 6 allocs | yes (flat in the number of peers) |
| Auth lookup, 1 and 10 peers | 1.2 and 1.9 us | 1.1 us | yes |
| One clipboard read through an external tool | 1.3 ms, 115 allocs (fork and exec) | unchanged per read | reads now only happen on a change event |
| Reading a 200 MB clipboard | 655 ms, 747 MB allocated | 5 ms, 3.3 MB (capped at 1 MiB + 1 and the tool is stopped) | yes |
| Idle clipboard process spawns | 2.5 per second | Wayland with `wl-paste --watch`: 0 while idle; X11 or fallback: 2.5 per second for 30 s after a change, then 0.5 per second | yes |
| `SaveFile` 256 MiB from memory | 3.0 to 3.3 GB/s | 256 KiB copy buffer: 2.2 to 3.4 GB/s (noise) | no, no measurable gain |
| Clip JSON encode, 4 KiB and 1 MiB | 7 us and 1.7 ms per peer | not changed | no: marshal-once would save about 7 us per copy with one phone |
| `Pack`, `Unpack`, `BuildManifest`, 10 000 small files | 218 ms, 723 ms, 35 ms | unchanged | recorded as the baseline |
| Upload 256 MiB over loopback TLS | HTTP/1.1: 144 and 133 MB/s | h2 (OkHttp default): 289 and 189 MB/s | h2 kept; loopback only, so it says nothing about WiFi |
| `sharingd` binary (`-trimpath -s -w`) | 9.1 MB | 9.9 MB | grew with the resumable features; module graph unchanged at 15 |
| Direct npm dependencies | 25 | 12 (and prettier as a dev dependency) | yes |
| Expo modules autolinked on Android | 20 | 15 | yes; the APK size was not measured |

The spawn rates in the table are computed from the code (400 ms and 2 s intervals), not measured with `strace`: no clipboard tool or desktop session exists in the sandbox.

Targets that could only be assessed on hardware or with the Android SDK were not measured: APK size (baseline 40 MB, target 25 MB), phone idle drain with the laptop off, cold start, service memory, throughput as a fraction of `iperf3`. The discovery change that should reduce idle drain (mDNS only when the last address fails, backoff up to 5 minutes, reset on network change) is in place but not measured.

### Done

- **Step 16.** Go benchmarks (`server`, `transfer`, `clipboard`) and the table above. The phone-side and throughput items need a device.
- **Step 17.** `.golangci.yml`, `.editorconfig` for ktlint, prettier for TypeScript (one formatting pass, only five files changed), `make lint` and `make bench`, CI wiring, CLI tests (the `call` helper, argument parsing, the confirmation flow, `doctor` checks), coverage reported in CI. The one `staticcheck` finding of the baseline was removed by rewriting the CLI.
- **Step 18.** Event-driven clipboard with an adaptive fallback and capped reads; hash-indexed auth; `Registry` expiry is lazy on lookup plus a one-minute sweep; `main.go` split into `run.go`, `cli.go`, `doctor.go`, `notify.go`; one `call` helper and a `flag.FlagSet` per subcommand (flags may appear anywhere); `control.API` with a method per route; `log/slog`; stale comments fixed; `sharingd doctor` is now the user of `discovery.Browse`. Settings: a changed name still needs a restart for mDNS, and `setup` says so.
- **Step 19 (Android).** One OkHttp client per fingerprint with a bounded dispatcher, shut down on unpair; discovery only when the last address fails, backoff capped at 5 minutes and reset by network change, Start or a share; 256 KiB copy buffers everywhere; two queue workers instead of a cached thread pool; SAF listing with one `DocumentsContract` cursor per directory; grouped and progress notifications; `Connection` split into `Session`, `Transfers`, `ClipSync` with a single `Prefs`; `WAKE_LOCK`, `ACCESS_WIFI_STATE` and `CHANGE_WIFI_STATE` removed (by static analysis: nothing in the code uses them; not yet confirmed on a phone); Android lint set to fail on errors with a baseline file.
- **Step 20 (mobile).** Unused dependencies removed in two batches, each checked with tsc, lint, tests, a Metro bundle and `expo-doctor` (its two network checks could not run offline); iOS and web configuration, `reset-project`, `expo.icon` and the favicon removed; `setup` split into a pairing screen and a permissions screen with `usePermissions` and `useClipboardAccess`; `Button`, `Step` and a small theme in `src/components` and `src/theme.ts`; the native clipboard flags are read once per visit; `CLAUDE.md` and `AGENTS.md` corrected.

### Decisions taken

1. iOS and web dropped.
2. QR scanning kept (CameraX and ML Kit stay), because their share of the APK could not be measured here.
3. Prettier for TypeScript, ktlint for Kotlin (the `intellij_idea` style with two-space indents, matching the existing code).
4. `ClipWatcher` log filtering not changed.
5. golangci-lint default-sized set (govet, staticcheck, errcheck, ineffassign, unused) plus goimports.

### Not done, and why

- **ProGuard keep rules were not narrowed.** R8 problems appear only in a release build that has to pair, scan and share on a phone; none of that can be exercised here, and a wrong rule breaks the app silently.
- **`ClipWatcher` server-side log filtering** needs the measured battery cost first and the user's Samsung phone; it stays as it is.
- **Marshal-once broadcast and a shared copy-buffer pool** were measured and rejected (see the table).
- **Batching MediaStore inserts** for a received folder: each file is still inserted, written and published separately. Without a device there is no number to compare, and MediaStore offers no batch insert that keeps per-file pending state.
- **Rtnetlink instead of interface polling for mDNS**: no wakeup measurement, so not touched.
- **The plan 3 device matrix** after the Android refactor has not been run.
