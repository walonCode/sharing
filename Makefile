# Build, install and test both halves of the project.
#   make help

BIN_DIR   ?= $(HOME)/.local/bin
DEST      ?= $(BIN_DIR)/sharingd
DIST      ?= dist
APK       := $(DIST)/sharing.apk
# Gradle breaks on very new JDKs (e.g. 27); point this at a JDK 17-21.
JAVA_HOME ?= $(HOME)/.local/share/mise/installs/java/temurin-21
ANDROID_HOME ?= $(HOME)/Android/Sdk
export JAVA_HOME ANDROID_HOME

# CPU architectures in the APK. arm64-v8a covers every modern phone and cuts the size sharply.
ABIS ?= arm64-v8a

GO_LDFLAGS := -s -w

.DEFAULT_GOAL := help
.PHONY: apk-quick apk-auto-clipboard help test test-api test-mobile lint lint-api lint-mobile daemon service setup daemon-uninstall apk phone logs clean bench

help: ## Show this help
	@awk 'BEGIN{FS=":.*## "} /^[a-zA-Z_-]+:.*## /{printf "  \033[1m%-18s\033[0m %s\n",$$1,$$2}' $(MAKEFILE_LIST)

## ---- checks ----

test: test-api test-mobile ## Run every check (use `make lint` for style checks)

test-api: ## go vet + go test -race
	cd api && go vet ./... && go test -race ./...

test-mobile: ## typecheck, lint, unit tests
	cd mobile && bunx tsc --noEmit && bunx expo lint && bun test src

lint: lint-api lint-mobile ## golangci-lint, ktlint and prettier

lint-api: ## golangci-lint (govet, staticcheck, errcheck, ineffassign, unused, goimports)
	cd api && golangci-lint run ./...

lint-mobile: ## prettier --check and ktlint (needs the ktlint CLI on PATH)
	cd mobile && bun run format:check
	cd mobile/modules/sharing-service/android && ktlint "src/**/*.kt"

bench: ## Go benchmarks (auth lookup, transfers, clipboard)
	cd api && go test ./internal/server ./internal/transfer ./internal/clipboard -run '^$$' -bench . -benchtime 3x

## ---- laptop ----

daemon: ## Build an optimised sharingd and install it to ~/.local/bin
	@mkdir -p $(BIN_DIR)
	cd api && go build -trimpath -ldflags="$(GO_LDFLAGS)" -o $(CURDIR)/$(DIST)/sharingd ./cmd/sharingd
	install -m 0755 $(DIST)/sharingd $(DEST)
	@# Restart so a running service picks up the new binary.
	@systemctl --user try-restart sharingd.service 2>/dev/null || true
	@echo "installed $(DEST)"

service: daemon ## Install sharingd as a systemd user service (boot, auto-restart)
	$(DEST) install

setup: ## Show the pairing QR (daemon must be running)
	$(DEST) setup

daemon-uninstall: ## Stop and remove the service and binary
	-$(DEST) uninstall
	rm -f $(DEST)

logs: ## Follow the daemon's logs
	journalctl --user -u sharingd.service -f

## ---- phone ----

apk: ## Build the minified release APK into dist/sharing.apk
	cd mobile && bun install --frozen-lockfile && bunx expo prebuild --platform android --clean --no-install
	cd mobile/android && ./gradlew assembleRelease -PreactNativeArchitectures=$(ABIS)
	@mkdir -p $(DIST)
	cp mobile/android/app/build/outputs/apk/release/app-release.apk $(APK)
	@ls -lh $(APK)

apk-auto-clipboard: ## Like `apk`, but declares READ_LOGS and SYSTEM_ALERT_WINDOW for the optional automatic clipboard
	SHARING_AUTO_CLIPBOARD=1 $(MAKE) apk

apk-quick: ## Rebuild the APK reusing mobile/android (fast; use after JS-only changes)
	cd mobile/android && ./gradlew assembleRelease -PreactNativeArchitectures=$(ABIS)
	@mkdir -p $(DIST)
	cp mobile/android/app/build/outputs/apk/release/app-release.apk $(APK)
	@ls -lh $(APK)

phone: apk ## Build the release APK and install it on the connected phone
	adb install -r $(APK)

phone-quick: apk-quick ## apk-quick, then install on the connected phone
	adb install -r $(APK)

clean: ## Remove build output
	rm -rf $(DIST) mobile/android
