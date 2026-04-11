#!/usr/bin/env bash
#
# Build RavenLink as a macOS .app bundle.
#
# Running the raw Go binary on macOS doesn't register with the Window
# Server, so the system tray icon won't appear. A proper .app bundle
# with LSUIElement=true makes the process a menu-bar-only accessory.
#
# Usage: ./scripts/build-macos.sh [--arch arm64|amd64|universal]
#
# Outputs:
#   dist/RavenLink.app/Contents/MacOS/ravenlink
#   dist/RavenLink.app/Contents/Info.plist
#
set -euo pipefail

# Ensure go is in PATH even when invoked from a non-login shell.
export PATH="${PATH}:/usr/local/go/bin:${HOME}/go/bin:/opt/homebrew/bin"

ARCH="${1:-arm64}"
case "$ARCH" in
  --arch) ARCH="$2" ;;
esac

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

APP_NAME="RavenLink"
APP_DIR="dist/${APP_NAME}.app"
MACOS_DIR="${APP_DIR}/Contents/MacOS"
RESOURCES_DIR="${APP_DIR}/Contents/Resources"
BIN_NAME="ravenlink"

echo "Building RavenLink for macOS (${ARCH})..."

rm -rf "${APP_DIR}"
mkdir -p "${MACOS_DIR}" "${RESOURCES_DIR}"

case "$ARCH" in
  arm64)
    GOARCH=arm64 CGO_ENABLED=1 go build -o "${MACOS_DIR}/${BIN_NAME}" ./cmd/ravenlink
    ;;
  amd64)
    GOARCH=amd64 CGO_ENABLED=1 go build -o "${MACOS_DIR}/${BIN_NAME}" ./cmd/ravenlink
    ;;
  universal)
    GOARCH=arm64 CGO_ENABLED=1 go build -o "${MACOS_DIR}/${BIN_NAME}-arm64" ./cmd/ravenlink
    GOARCH=amd64 CGO_ENABLED=1 go build -o "${MACOS_DIR}/${BIN_NAME}-amd64" ./cmd/ravenlink
    lipo -create -output "${MACOS_DIR}/${BIN_NAME}" \
      "${MACOS_DIR}/${BIN_NAME}-arm64" "${MACOS_DIR}/${BIN_NAME}-amd64"
    rm "${MACOS_DIR}/${BIN_NAME}-arm64" "${MACOS_DIR}/${BIN_NAME}-amd64"
    ;;
  *)
    echo "Unknown arch: $ARCH (expected arm64, amd64, or universal)"
    exit 1
    ;;
esac

cat > "${APP_DIR}/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDevelopmentRegion</key>
    <string>en</string>
    <key>CFBundleExecutable</key>
    <string>${BIN_NAME}</string>
    <key>CFBundleIdentifier</key>
    <string>ca.team1310.ravenlink</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>CFBundleName</key>
    <string>${APP_NAME}</string>
    <key>CFBundleDisplayName</key>
    <string>${APP_NAME}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>0.1.0</string>
    <key>CFBundleVersion</key>
    <string>0.1.0</string>
    <key>LSMinimumSystemVersion</key>
    <string>11.0</string>
    <!-- LSUIElement=true makes this a menu-bar-only accessory app:
         no dock icon, no main window, just the systray icon. -->
    <key>LSUIElement</key>
    <true/>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
EOF

echo "Built ${APP_DIR}"
echo ""
echo "To run:"
echo "  open ${APP_DIR}"
echo "Or directly:"
echo "  ${MACOS_DIR}/${BIN_NAME} --team 1310"
echo ""
echo "The menu bar icon should appear after 'open' because macOS will"
echo "register the bundle with the Window Server."
