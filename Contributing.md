# Contributing

Thanks for helping. The project is small: a Go daemon for the laptop (`api/`) and an Expo app with a Kotlin background service for Android (`mobile/`). Start with the architecture section in [README.md](README.md) and the design notes in `docs/`.

## Workflow

1. Open an issue first for anything bigger than a small fix.
2. Branch from `main` (`feat/…`, `fix/…`, `docs/…`).
3. Write the code **and its tests**, run the checks below, then open a pull request.
4. PRs are merged into `main`.

Commit messages should say what changed and why: a short imperative summary line, then a body when the reason is not obvious. "Fix clipboard echo when the phone sends the same text twice" is useful; "fixes" is not.

## Setup and checks

The `Makefile` wraps the common tasks (`make help`): `make test` runs every check for both projects, `make service` builds and installs the daemon, `make phone` builds the release APK and installs it on a USB-connected phone, and `make phone-quick` is a fast rebuild that reuses the generated `mobile/android` (use `make apk` or `make phone` after changing `app.json`, plugins, or native dependencies). Gradle needs JDK 17-21: pass `JAVA_HOME=/path/to/jdk21`.

### Laptop daemon (`api/`)

```bash
cd api
go vet ./... && go test -race ./...
go test ./internal/transfer -run TestFolderRoundTrip    # a single test
go run ./cmd/sharingd run                                # SHARING_CONFIG_DIR=/tmp/x keeps your real config untouched
```

Layout: `internal/{config,pairing,protocol,server,discovery,transfer,clipboard,control,service}` and `cmd/sharingd`. Tests sit next to the code and use temporary directories; please keep it that way (no tests that touch `~/.config` or the real clipboard without restoring it).

### Phone app (`mobile/`)

```bash
cd mobile
bun install
bunx tsc --noEmit
bunx expo lint
bun test src
bunx expo-doctor
```

To compile the Kotlin module without a device (use JDK 21; newer JDKs break Gradle):

```bash
bunx expo prebuild --platform android --no-install
cd android && JAVA_HOME=/path/to/jdk21 ./gradlew :sharing-service:compileDebugKotlin
```

Things to know:

- Expo changes between SDK releases. Check the docs for the SDK version in `package.json` before using an API; add packages with `bunx expo install <pkg>`.
- The background service must work with no JavaScript running, so logic belongs in Kotlin (`mobile/modules/sharing-service`). The module's JS surface stays thin.
- `mobile/android/` is generated and not committed.
- Android 10+ restrictions (clipboard reads, foreground-service types) shape the design; see `docs/plan2.md` before changing them. The optional automatic clipboard (`ClipWatcher.kt`) depends on system log wording that differs between AOSP and Samsung; test it on a real device and read the README section before changing it.
- Kotlin changes cannot be fully verified by compiling. If you touch the service, install it on a phone (`make phone-quick`) and check `sharingd peers`, a transfer each way, and the clipboard. `adb logcat -s SharingClip` shows the clipboard watcher.
- `docs/plan1.md` and `docs/plan2.md` are the original build plans. The README is the source of truth for how things work today.

## Guidelines

- Keep changes focused; unrelated cleanups belong in their own PR.
- Anything that touches pairing, auth, paths, or archive handling needs a test for the failure case (expired token, path traversal, and so on). Read [Security.md](Security.md) first.
- Never commit certificates, tokens, or your `~/.config/sharing`.
- Match the surrounding code style; run `gofmt` for Go.

## Reporting bugs

Include your Linux distro and desktop session (Wayland/X11), phone model and Android version, and the output of `journalctl --user -u sharingd -n 50`. For security issues follow [Security.md](Security.md) instead of opening an issue.

## License

By contributing you agree that your work is released under the [MIT License](LICENSE).
