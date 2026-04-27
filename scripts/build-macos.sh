#!/usr/bin/env bash
#
# Build RavenLink as a macOS .app bundle.
#
# Running the raw Go binary on macOS doesn't register with the Window
# Server, so the system tray icon won't appear. A proper .app bundle
# with LSUIElement=true makes the process a menu-bar-only accessory.
#
# Usage: ./scripts/build-macos.sh [arm64|amd64|universal]
#
# Outputs:
#   dist/RavenLink.app/Contents/MacOS/ravenlink
#   dist/RavenLink.app/Contents/Info.plist
#   dist/RavenLink.app/Contents/Resources/RavenLink.icns
#
set -euo pipefail

export PATH="${PATH}:/usr/local/go/bin:${HOME}/go/bin:/opt/homebrew/bin"

ARCH="${1:-arm64}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Read the version from internal/version/version.go so Info.plist stays
# in sync with the in-binary version constant. VERSION env var wins.
VERSION="${VERSION:-$(grep -E '^const Version' internal/version/version.go | sed -E 's/.*"([^"]+)".*/\1/')}"
if [[ -z "$VERSION" ]]; then
  echo "Could not determine VERSION from internal/version/version.go" >&2
  exit 1
fi

APP_NAME="RavenLink"
APP_DIR="dist/${APP_NAME}.app"
MACOS_DIR="${APP_DIR}/Contents/MacOS"
RESOURCES_DIR="${APP_DIR}/Contents/Resources"
ICONSET_DIR="dist/${APP_NAME}.iconset"
ICNS_PATH="${RESOURCES_DIR}/${APP_NAME}.icns"
BIN_NAME="ravenlink"

echo "Building RavenLink for macOS (${ARCH})..."

rm -rf "${APP_DIR}" "${ICONSET_DIR}"
mkdir -p "${MACOS_DIR}" "${RESOURCES_DIR}"

# --- Binary ---
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

# --- Icon ---
echo "Generating icon set..."
go run ./cmd/iconbuilder "${ICONSET_DIR}"

if command -v iconutil >/dev/null 2>&1; then
  iconutil -c icns "${ICONSET_DIR}" -o "${ICNS_PATH}"
  echo "Wrote ${ICNS_PATH}"
else
  echo "warning: iconutil not found — Dock/Activity Monitor will show a generic icon"
fi

# --- Info.plist ---
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
    <string>${VERSION}</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>CFBundleIconFile</key>
    <string>${APP_NAME}</string>
    <key>LSMinimumSystemVersion</key>
    <string>11.0</string>
    <!-- LSUIElement=true makes RavenLink a menu-bar-only "accessory"
         app: no Dock icon, no app menu, no ⌘-Tab entry. The user
         opens the dashboard via the menu bar icon's "Open Dashboard"
         item or by re-launching the app (which re-opens the browser
         from main.go). -->
    <key>LSUIElement</key>
    <true/>
    <key>NSPrincipalClass</key>
    <string>NSApplication</string>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
EOF

echo ""
echo "Built ${APP_DIR}"
echo ""
echo "To run:"
echo "  open ${APP_DIR}"
echo ""
echo "On first run, RavenLink will open a browser to the dashboard"
echo "at http://localhost:8080 for initial configuration."
