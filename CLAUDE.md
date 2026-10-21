# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository state

Early-stage monorepo with two independent projects (no root build tooling, not a git repo yet; root `README.md` is empty):

- `api/` — Go module `api` (`go 1.27.2`). Currently only a stub `main.go`; no dependencies, packages, or tests yet.
- `mobile/` — Expo / React Native app (Expo SDK 58, React 19.3, RN 0.88, TypeScript, bun lockfile). Source is under `mobile/src/` (`app/` holds Expo Router routes, `lib/` is currently empty).

## Workflow rules

- Never put co-author lines, the Claude name, or "generated with" notes in commits, PRs, or any other artifact.
- Flow per change: branch, code, test, commit (descriptive message), PR, merge into `main`. Never delete branches after merge, locally or on the remote.
- Ask the user when design decisions are open.
- Stop any services/processes started during work (dev servers, daemons, emulators) before finishing.

## Product goal

AirDrop-like sharing between a Linux laptop and an Android phone over WiFi: files, folders, images, videos, plus automatic two-way clipboard sync over websocket. The app is opened only once for setup (destination folder, pairing); afterwards both sides run as always-on background services with mDNS discovery (no fixed IPs) and a pairing-code-derived shared key. Chosen design: Go daemon in `api/` run as a systemd user service; Android via the Expo app plus a custom Kotlin foreground-service native module (needs a development build, not Expo Go).

## Commands

API (run from `api/`):

```bash
go run .
go build ./...
go test ./...
go test ./path/to/pkg -run TestName   # single test
```

Mobile (run from `mobile/`; bun is used, so prefer `bunx` over `npx`):

```bash
bun install
bun run start            # expo start (also: android, ios, web scripts)
bun run lint             # expo lint
bunx tsc --noEmit        # typecheck
bunx expo install <pkg>  # ALWAYS use instead of bun add — resolves SDK-compatible versions
bunx expo-doctor         # diagnose dependency/config issues
```

There is no test runner configured for either project. Run lint and typecheck before declaring mobile work done.

## Mobile conventions (from `mobile/AGENTS.md`)

- Expo changes breaking APIs every SDK: do not rely on memory. Check the `expo` major version in `package.json` and consult `https://docs.expo.dev/versions/v<major>.0.0/` (index: `https://docs.expo.dev/llms.txt`) before touching Expo/EAS/RN APIs.
- Navigation is Expo Router only. Every file in `src/app/` is a route (`_layout.tsx` defines navigators); keep components, hooks, and utils outside `src/app/` (e.g. in `src/lib/`).
- `ios/` and `android/` are generated (Continuous Native Generation) when absent — never create or edit them by hand; configure native behavior via `app.json` and config plugins.
- Expo Go only bundles certain native modules; after adding a library with native code use a development build (`bunx expo run:ios|android` or `bunx eas-cli build --profile development`).
- Prefer official Expo modules over third-party libraries. Builds/OTA go through EAS (`bunx eas-cli build|submit|update`).
- Mobile-first: prioritize performance and cross-platform compatibility.
