#!/usr/bin/env bash
#
# build.sh — install prerequisites and build Signal Station.
#
# Run this on macOS to produce BOTH executables:
#   - Signal Station.app  (universal: Apple Silicon + Intel)
#   - Signal Station.exe  (Windows x86-64, cross-compiled via mingw-w64)
#
# On Linux it builds the Windows executable and a native Linux binary. It cannot
# build the macOS one: that needs Apple's SDK, which their licence restricts to
# Apple hardware. Use a Mac or the GitHub Actions workflow for that.
#
#   ./build.sh              build everything this host can
#   ./build.sh --mac        macOS only
#   ./build.sh --windows    Windows only
#   ./build.sh --skip-deps  don't touch package managers, just build
#   ./build.sh --clean      remove dist/ first

set -euo pipefail

APP_NAME="Signal Station"
APP_ID="org.signalstation.app"
APP_VERSION="1.0.0"
BIN_NAME="signal-station"

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DIST="$PWD/dist"

HOST="$(uname -s)"
BUILD_MAC=0
BUILD_WIN=0
BUILD_LINUX=0
SKIP_DEPS=0
EXPLICIT=0

for arg in "$@"; do
  case "$arg" in
    --mac|--macos|--darwin) BUILD_MAC=1; EXPLICIT=1 ;;
    --windows|--win)        BUILD_WIN=1; EXPLICIT=1 ;;
    --linux)                BUILD_LINUX=1; EXPLICIT=1 ;;
    --skip-deps)            SKIP_DEPS=1 ;;
    --clean)                rm -rf "$DIST" "$APP_NAME.app" "$APP_NAME.exe" ;;
    -h|--help)              awk 'NR>1 && /^#/ {sub(/^# ?/,""); print; next} NR>1 {exit}' "$0"; exit 0 ;;
    *) echo "Unknown option: $arg (try --help)" >&2; exit 2 ;;
  esac
done

if [ "$EXPLICIT" -eq 0 ]; then
  BUILD_WIN=1
  case "$HOST" in
    Darwin) BUILD_MAC=1 ;;
    Linux)  BUILD_LINUX=1 ;;
  esac
fi

if [ "$BUILD_MAC" -eq 1 ] && [ "$HOST" != "Darwin" ]; then
  echo "!! Cannot build the macOS app on $HOST." >&2
  echo "   Fyne needs cgo against Apple's SDK, and Apple's licence restricts that" >&2
  echo "   SDK to Apple hardware. Build on a Mac, or push and run the GitHub" >&2
  echo "   Actions workflow in .github/workflows/build.yml." >&2
  exit 1
fi

# ---------------------------------------------------------------- output helpers

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
step()  { printf '\n\033[1;36m==>\033[0m \033[1m%s\033[0m\n' "$*"; }
info()  { printf '    %s\n' "$*"; }
warn()  { printf '\033[1;33m !! %s\033[0m\n' "$*"; }
die()   { printf '\033[1;31m !! %s\033[0m\n' "$*" >&2; exit 1; }
have()  { command -v "$1" >/dev/null 2>&1; }

# ------------------------------------------------------------------ prerequisites

# need_root resolves how to escalate for package installs: nothing when already
# root (containers, CI images), sudo when available, otherwise a clear refusal
# rather than "sudo: command not found".
need_root() {
  if [ "$(id -u)" -eq 0 ]; then SUDO=""; return 0; fi
  if have sudo; then SUDO="sudo"; return 0; fi
  return 1
}

SUDO=""

install_deps() {
  step "Checking prerequisites"

  if [ "$HOST" = "Darwin" ]; then
    # Fyne compiles cgo against the system toolchain, so the Xcode command line
    # tools are non-optional even for the Windows cross-build.
    if ! xcode-select -p >/dev/null 2>&1; then
      warn "Xcode command line tools are missing. Launching the installer…"
      xcode-select --install || true
      die "Finish the Xcode installer, then run this script again."
    fi
    info "Xcode command line tools: ok"

    if ! have brew; then
      warn "Homebrew is not installed, and it is how this script fetches Go and mingw-w64."
      echo
      echo '    /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'
      echo
      die "Install Homebrew with the line above, then re-run this script."
    fi
  fi

  if ! have go; then
    step "Installing Go"
    case "$HOST" in
      Darwin) brew install go ;;
      Linux)
        need_root || die "Go is missing and this script cannot install it without root. Install Go 1.22+ from https://go.dev/dl/"
        if   have apt-get; then $SUDO apt-get update || warn "apt-get update reported errors; continuing anyway"
                                $SUDO apt-get install -y golang-go
        elif have dnf;     then $SUDO dnf install -y golang
        elif have pacman;  then $SUDO pacman -S --noconfirm go
        else die "Install Go 1.22 or newer from https://go.dev/dl/ and re-run."
        fi ;;
    esac
  fi
  have go || die "Go is still not on PATH."

  # Fyne 2.6 needs Go 1.22+. Catch an old distro package now rather than as a
  # confusing compile error twenty seconds in.
  local gov major minor
  gov="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
  major="${gov%%.*}"; minor="$(echo "$gov" | cut -d. -f2)"
  if [ "${major:-0}" -lt 1 ] || { [ "${major:-0}" -eq 1 ] && [ "${minor:-0}" -lt 22 ]; }; then
    die "Go $gov is too old; this needs 1.22 or newer. Get it from https://go.dev/dl/"
  fi
  info "Go $gov: ok"

  if [ "$BUILD_WIN" -eq 1 ] && ! have x86_64-w64-mingw32-gcc; then
    step "Installing mingw-w64 (the Windows cross-compiler)"
    case "$HOST" in
      Darwin) brew install mingw-w64 ;;
      Linux)
        need_root || die "mingw-w64 is missing and installing it needs root. Install gcc-mingw-w64-x86-64, or re-run with --linux."
        if   have apt-get; then $SUDO apt-get update || warn "apt-get update reported errors; continuing anyway"
                                $SUDO apt-get install -y gcc-mingw-w64-x86-64
        elif have dnf;     then $SUDO dnf install -y mingw64-gcc
        elif have pacman;  then $SUDO pacman -S --noconfirm mingw-w64-gcc
        else die "Install mingw-w64 yourself, or re-run with --mac to skip Windows."
        fi ;;
    esac
  fi
  # Verify rather than assume: a package manager can report success while the
  # binary lands under a name or path that is not on PATH.
  if [ "$BUILD_WIN" -eq 1 ]; then
    have x86_64-w64-mingw32-gcc \
      || die "mingw-w64 still is not on PATH after installing. Install it manually, or re-run without the Windows target."
    info "mingw-w64: ok"
  fi

  step "Installing the fyne packaging tool"
  # This moved out of the main module at Fyne 2.5; fyne.io/fyne/v2/cmd/fyne is
  # deprecated and will not build against current releases.
  #
  # Non-fatal: the tool only embeds icons and bundles the .app. Without it the
  # script still produces working executables, so an offline or blocked install
  # should not abort the build.
  if go install fyne.io/tools/cmd/fyne@latest; then
    info "fyne tool: ok"
  else
    warn "Could not install the fyne tool. Builds will proceed without icon embedding."
  fi
}

FYNE_BIN=""
resolve_fyne() {
  if have fyne; then FYNE_BIN="$(command -v fyne)"; return; fi
  local gobin
  gobin="$(go env GOBIN)"; [ -n "$gobin" ] || gobin="$(go env GOPATH)/bin"
  [ -x "$gobin/fyne" ] && FYNE_BIN="$gobin/fyne"
}

# ------------------------------------------------------------------------- builds

build_mac() {
  step "Building for macOS (universal)"
  mkdir -p "$DIST"

  # Build each architecture, then fuse them so one download runs on both Apple
  # Silicon and Intel.
  CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
    go build -trimpath -ldflags "-s -w" -o "$DIST/$BIN_NAME-arm64" .
  info "arm64: done"

  CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o "$DIST/$BIN_NAME-amd64" .
  info "amd64: done"

  lipo -create -output "$DIST/$BIN_NAME" "$DIST/$BIN_NAME-arm64" "$DIST/$BIN_NAME-amd64"
  rm -f "$DIST/$BIN_NAME-arm64" "$DIST/$BIN_NAME-amd64"
  info "$(lipo -info "$DIST/$BIN_NAME")"

  step "Bundling $APP_NAME.app"
  rm -rf "$DIST/$APP_NAME.app"
  if [ -n "$FYNE_BIN" ] && "$FYNE_BIN" package -os darwin \
        -executable "$DIST/$BIN_NAME" -icon Icon.png \
        -name "$APP_NAME" -appID "$APP_ID" -appVersion "$APP_VERSION" 2>/dev/null; then
    mv "$APP_NAME.app" "$DIST/" 2>/dev/null || true
  else
    warn "fyne package did not run; assembling the bundle by hand."
    hand_bundle_mac
  fi

  # Declare the signalcaptcha:// URL scheme so clicking "Open Signal" on the
  # captcha page launches Signal Station with the token. The fyne packager does
  # not add custom schemes, so patch the bundle's Info.plist directly.
  local plist="$DIST/$APP_NAME.app/Contents/Info.plist"
  if [ -f "$plist" ] && have /usr/libexec/PlistBuddy; then
    /usr/libexec/PlistBuddy -c "Add :CFBundleURLTypes array" "$plist" 2>/dev/null || true
    /usr/libexec/PlistBuddy -c "Add :CFBundleURLTypes:0 dict" "$plist" 2>/dev/null || true
    /usr/libexec/PlistBuddy -c "Add :CFBundleURLTypes:0:CFBundleURLName string org.signalstation.captcha" "$plist" 2>/dev/null || true
    /usr/libexec/PlistBuddy -c "Add :CFBundleURLTypes:0:CFBundleURLSchemes array" "$plist" 2>/dev/null || true
    /usr/libexec/PlistBuddy -c "Add :CFBundleURLTypes:0:CFBundleURLSchemes:0 string signalcaptcha" "$plist" 2>/dev/null || true
    echo "    declared signalcaptcha:// URL scheme"
  fi

  # Ad-hoc signature. Unsigned binaries are killed outright on Apple Silicon;
  # ad-hoc signing gets you the right-click-Open path instead.
  codesign --force --deep --sign - "$DIST/$APP_NAME.app" 2>/dev/null \
    || warn "codesign failed; users will need: xattr -dr com.apple.quarantine '$APP_NAME.app'"

  ( cd "$DIST" && ditto -c -k --keepParent "$APP_NAME.app" "SignalStation-macOS.zip" )
  bold "  → $DIST/$APP_NAME.app"
  bold "  → $DIST/SignalStation-macOS.zip"
}

# hand_bundle_mac assembles a minimal .app if the fyne tool is unavailable.
hand_bundle_mac() {
  local app="$DIST/$APP_NAME.app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  cp "$DIST/$BIN_NAME" "$app/Contents/MacOS/$BIN_NAME"
  chmod +x "$app/Contents/MacOS/$BIN_NAME"

  if have sips && have iconutil; then
    local ic="$DIST/icon.iconset"; mkdir -p "$ic"
    for sz in 16 32 64 128 256 512; do
      sips -z $sz $sz Icon.png --out "$ic/icon_${sz}x${sz}.png" >/dev/null 2>&1 || true
      sips -z $((sz*2)) $((sz*2)) Icon.png --out "$ic/icon_${sz}x${sz}@2x.png" >/dev/null 2>&1 || true
    done
    iconutil -c icns "$ic" -o "$app/Contents/Resources/Icon.icns" 2>/dev/null || true
    rm -rf "$ic"
  fi

  cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>$APP_NAME</string>
  <key>CFBundleDisplayName</key><string>$APP_NAME</string>
  <key>CFBundleExecutable</key><string>$BIN_NAME</string>
  <key>CFBundleIdentifier</key><string>$APP_ID</string>
  <key>CFBundleVersion</key><string>$APP_VERSION</string>
  <key>CFBundleShortVersionString</key><string>$APP_VERSION</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleIconFile</key><string>Icon.icns</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>CFBundleURLTypes</key>
  <array>
    <dict>
      <key>CFBundleURLName</key><string>org.signalstation.captcha</string>
      <key>CFBundleURLSchemes</key>
      <array><string>signalcaptcha</string></array>
    </dict>
  </array>
</dict>
</plist>
PLIST
}

build_windows() {
  step "Building for Windows (x86-64)"
  mkdir -p "$DIST"

  local cc="x86_64-w64-mingw32-gcc"
  local cxx="x86_64-w64-mingw32-g++"
  # Running under Git Bash or MSYS2 on Windows itself: the native gcc is the
  # right compiler, not the cross one. A glob will not match inside [ ], so this
  # has to be a case.
  case "$HOST" in
    MINGW*|MSYS*|CYGWIN*) cc=gcc; cxx=g++ ;;
  esac
  have "$cc" || die "$cc not found. Install mingw-w64, or re-run with --mac."

  rm -f "$DIST/$APP_NAME.exe"
  if [ -n "$FYNE_BIN" ] && \
     CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="$cc" CXX="$cxx" \
     "$FYNE_BIN" package -os windows -icon Icon.png \
        -name "$APP_NAME" -appID "$APP_ID" -appVersion "$APP_VERSION" 2>/dev/null; then
    mv "$APP_NAME.exe" "$DIST/" 2>/dev/null || true
    bold "  → $DIST/$APP_NAME.exe  (icon embedded)"
  else
    # Plain build. -H=windowsgui keeps a console window from opening behind it.
    warn "fyne package did not run; building a plain executable without the embedded icon."
    CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="$cc" CXX="$cxx" \
      go build -trimpath -ldflags "-s -w -H=windowsgui" -o "$DIST/$BIN_NAME.exe" .
    bold "  → $DIST/$BIN_NAME.exe"
  fi
}

build_linux() {
  step "Building for Linux"
  mkdir -p "$DIST"
  CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o "$DIST/$BIN_NAME-linux" .
  bold "  → $DIST/$BIN_NAME-linux"
}

# ---------------------------------------------------------------------------- go

[ "$SKIP_DEPS" -eq 1 ] || install_deps
resolve_fyne
[ -n "$FYNE_BIN" ] || warn "fyne tool not on PATH; falling back to plain builds."

# Duplicate source files are the most common failure when the files were
# downloaded individually: a browser names a second copy "signalcli(1).go", and
# Go compiles every .go file in the directory, so each declaration appears
# twice. The resulting "redeclared in this block" wall says nothing useful.
dupes="$(find . -maxdepth 1 -type f -name '*.go' \
         \( -name '*([0-9])*.go' -o -name '*[Cc]opy*.go' \) 2>/dev/null || true)"
if [ -n "$dupes" ]; then
  warn "Duplicate Go source files found. Go compiles every .go file in this folder:"
  echo "$dupes" | sed 's/^/      /'
  info ""
  info "Delete them with:"
  info "    rm ./*\\(*\\).go"
  die "Duplicate source files."
fi

step "Resolving Go modules"
go mod tidy

mkdir -p "$DIST"
[ "$BUILD_MAC" -eq 1 ]   && build_mac
[ "$BUILD_WIN" -eq 1 ]   && build_windows
[ "$BUILD_LINUX" -eq 1 ] && build_linux

step "Done"
ls -lh "$DIST" | tail -n +2 | awk '{printf "    %-38s %s\n", $9, $5}'

if [ "$BUILD_MAC" -eq 0 ] && [ "$HOST" != "Darwin" ]; then
  echo
  info "No macOS build: that requires a Mac. The GitHub Actions workflow in"
  info ".github/workflows/build.yml will produce one on a hosted runner."
fi
