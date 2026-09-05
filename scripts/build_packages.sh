#!/usr/bin/env bash
set -e

VERSION="${GITHUB_REF_NAME:-}"
if [ -z "${VERSION}" ] || [ "${VERSION}" = "main" ] || [ "${VERSION}" = "master" ]; then
  VERSION=$(git describe --tags --abbrev=0 2>/dev/null || echo "v1.0.0")
fi
RAW_VERSION="${VERSION#v}"

echo "=========================================="
echo " Building Limoni Voice ${VERSION} (${RAW_VERSION})"
echo "=========================================="

rm -rf release_assets dist
mkdir -p release_assets
mkdir -p dist/linux-amd64 dist/linux-arm64
mkdir -p dist/windows-amd64 dist/windows-arm64
mkdir -p dist/darwin-arm64 dist/darwin-amd64

LDFLAGS="-s -w -X main.AppVersion=${VERSION}"

# 0. Prep installer dependencies
mkdir -p cmd/installer
touch cmd/installer/limoni-voice.exe
cp microphone.obj cmd/installer/microphone.obj || true
cp assets/icon.ico cmd/installer/icon.ico || true

# 1. Run Unit Tests
echo "==> Running Unit Tests..."
go test -mod=vendor -v ./...

# 2. Compile Linux AMD64 & ARM64
echo "==> Building Linux AMD64..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -ldflags="${LDFLAGS}" -o dist/linux-amd64/limoni-voice .

echo "==> Building Linux ARM64..."
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=vendor -ldflags="${LDFLAGS}" -o dist/linux-arm64/limoni-voice .

# 3. Compile Windows AMD64 & ARM64
echo "==> Building Windows AMD64 (.exe)..."
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=vendor -ldflags="${LDFLAGS}" -o dist/windows-amd64/limoni-voice.exe .
cp dist/windows-amd64/limoni-voice.exe release_assets/limoni-voice_${VERSION}_windows_amd64.exe
cp dist/windows-amd64/limoni-voice.exe release_assets/limoni-voice_windows_amd64.exe

echo "==> Building Windows ARM64 (.exe)..."
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -mod=vendor -ldflags="${LDFLAGS}" -o dist/windows-arm64/limoni-voice.exe .
cp dist/windows-arm64/limoni-voice.exe release_assets/limoni-voice_${VERSION}_windows_arm64.exe
cp dist/windows-arm64/limoni-voice.exe release_assets/limoni-voice_windows_arm64.exe

echo "==> Building Windows Setup Installer (.exe)..."
cp microphone.obj cmd/installer/microphone.obj
cp assets/icon.ico cmd/installer/icon.ico
cp dist/windows-amd64/limoni-voice.exe cmd/installer/limoni-voice.exe
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=vendor -ldflags="${LDFLAGS}" -o release_assets/Limoni-Voice-Setup_windows_amd64.exe ./cmd/installer
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=vendor -ldflags="${LDFLAGS}" -o release_assets/Limoni-Voice-Setup.exe ./cmd/installer

cp dist/windows-arm64/limoni-voice.exe cmd/installer/limoni-voice.exe
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -mod=vendor -ldflags="${LDFLAGS}" -o release_assets/Limoni-Voice-Setup_windows_arm64.exe ./cmd/installer
rm -f cmd/installer/limoni-voice.exe
touch cmd/installer/limoni-voice.exe

# 4. Compile macOS (Darwin) Apple Silicon (ARM64) & Intel (AMD64)
echo "==> Building macOS Apple Silicon (ARM64)..."
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -mod=vendor -ldflags="${LDFLAGS}" -o dist/darwin-arm64/limoni-voice .

echo "==> Building macOS Intel (AMD64)..."
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -mod=vendor -ldflags="${LDFLAGS}" -o dist/darwin-amd64/limoni-voice .

## 5. Package Linux Tarballs (.tar.gz)
echo "==> Packaging Linux Tarballs..."
mkdir -p dist/pkg-linux-amd64 dist/pkg-linux-arm64
cp dist/linux-amd64/limoni-voice README.md LICENSE microphone.obj dist/pkg-linux-amd64/
tar -czf "release_assets/limoni-voice_${VERSION}_linux_amd64.tar.gz" -C dist/pkg-linux-amd64 .

cp dist/linux-arm64/limoni-voice README.md LICENSE microphone.obj dist/pkg-linux-arm64/
tar -czf "release_assets/limoni-voice_${VERSION}_linux_arm64.tar.gz" -C dist/pkg-linux-arm64 .

# 6. Package macOS Native Application Bundles (.app.zip & .app.tar.gz)
build_macos_app() {
  local ARCH=$1
  local MAC_ARCH=$2
  local APP_NAME="Limoni Voice.app"
  local APP_DIR="dist/app-${MAC_ARCH}/${APP_NAME}"

  echo "==> Building macOS Application Bundle (${MAC_ARCH})..."
  rm -rf "dist/app-${MAC_ARCH}"
  mkdir -p "${APP_DIR}/Contents/MacOS"
  mkdir -p "${APP_DIR}/Contents/Resources"

  cp "dist/${ARCH}/limoni-voice" "${APP_DIR}/Contents/MacOS/limoni-voice"
  chmod 755 "${APP_DIR}/Contents/MacOS/limoni-voice"
  cp README.md LICENSE microphone.obj "${APP_DIR}/Contents/MacOS/"

  cat <<PLIST_EOF > "${APP_DIR}/Contents/Info.plist"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>limoni-voice-launcher</string>
    <key>CFBundleIdentifier</key>
    <string>com.thebanri.limonivoice</string>
    <key>CFBundleName</key>
    <string>Limoni Voice</string>
    <key>CFBundleDisplayName</key>
    <string>Limoni Voice</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>${RAW_VERSION}</string>
    <key>CFBundleVersion</key>
    <string>${RAW_VERSION}</string>
    <key>LSMinimumSystemVersion</key>
    <string>10.13</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSMicrophoneUsageDescription</key>
    <string>Limoni Voice requires microphone access for real-time P2P encrypted voice chat.</string>
</dict>
</plist>
PLIST_EOF

  echo -n "APPL????" > "${APP_DIR}/Contents/PkgInfo"

  cat <<'LAUNCHER_EOF' > "${APP_DIR}/Contents/MacOS/limoni-voice-launcher"
#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
osascript <<EOF
tell application "Terminal"
    activate
    do script "cd \"$DIR\" && clear && \"$DIR/limoni-voice\"; exit"
end tell
EOF
LAUNCHER_EOF
  chmod 755 "${APP_DIR}/Contents/MacOS/limoni-voice-launcher"

  # Package into .app.zip and .app.tar.gz
  (
    cd "dist/app-${MAC_ARCH}"
    if command -v zip >/dev/null 2>&1; then
      zip -r -y "../../release_assets/Limoni-Voice_${VERSION}_macOS_${MAC_ARCH}.app.zip" "${APP_NAME}"
    elif command -v 7z >/dev/null 2>&1; then
      7z a -tzip "../../release_assets/Limoni-Voice_${VERSION}_macOS_${MAC_ARCH}.app.zip" "${APP_NAME}"
    fi
    tar -czf "../../release_assets/Limoni-Voice_${VERSION}_macOS_${MAC_ARCH}.app.tar.gz" "${APP_NAME}"
  )

  # Package into .dmg (Apple Disk Image) with Drag-and-Drop to /Applications
  local DMG_STAGE="dist/dmg-${MAC_ARCH}"
  rm -rf "${DMG_STAGE}"
  mkdir -p "${DMG_STAGE}"
  cp -R "${APP_DIR}" "${DMG_STAGE}/"
  ln -s /Applications "${DMG_STAGE}/Applications"

  if command -v genisoimage >/dev/null 2>&1; then
    echo "==> Creating macOS DMG (${MAC_ARCH})..."
    genisoimage -V "Limoni Voice" -D -R -apple -no-pad -quiet -o "release_assets/Limoni-Voice_${VERSION}_macOS_${MAC_ARCH}.dmg" "${DMG_STAGE}"
  elif command -v mkisofs >/dev/null 2>&1; then
    echo "==> Creating macOS DMG (${MAC_ARCH})..."
    mkisofs -V "Limoni Voice" -D -R -apple -no-pad -quiet -o "release_assets/Limoni-Voice_${VERSION}_macOS_${MAC_ARCH}.dmg" "${DMG_STAGE}"
  elif command -v hdiutil >/dev/null 2>&1; then
    echo "==> Creating macOS DMG (${MAC_ARCH})..."
    hdiutil create -volname "Limoni Voice" -srcfolder "${DMG_STAGE}" -ov -format UDZO "release_assets/Limoni-Voice_${VERSION}_macOS_${MAC_ARCH}.dmg"
  fi
}

build_macos_app "darwin-arm64" "arm64"
build_macos_app "darwin-amd64" "amd64"

# 7. Package macOS CLI Tarballs & Zips
echo "==> Packaging macOS CLI Binaries..."
mkdir -p dist/pkg-darwin-arm64 dist/pkg-darwin-amd64
cp dist/darwin-arm64/limoni-voice README.md LICENSE microphone.obj dist/pkg-darwin-arm64/
tar -czf "release_assets/limoni-voice_${VERSION}_darwin_arm64.tar.gz" -C dist/pkg-darwin-arm64 .

cp dist/darwin-amd64/limoni-voice README.md LICENSE microphone.obj dist/pkg-darwin-amd64/
tar -czf "release_assets/limoni-voice_${VERSION}_darwin_amd64.tar.gz" -C dist/pkg-darwin-amd64 .

# 8. Windows Packaging (Setup Installer Only)
echo "==> Windows Setup Installer packaged into release_assets/Limoni-Voice-Setup.exe"

# 9. Build Debian Packages (.deb)
build_deb() {
  local ARCH=$1
  local DEB_ARCH=$2
  local PKG_DIR="deb_pkg_${DEB_ARCH}"

  if command -v dpkg-deb >/dev/null 2>&1; then
    echo "==> Building Debian Package (${DEB_ARCH})..."
    rm -rf "${PKG_DIR}"
    mkdir -p "${PKG_DIR}/DEBIAN"
    mkdir -p "${PKG_DIR}/usr/bin"
    mkdir -p "${PKG_DIR}/usr/share/applications"
    mkdir -p "${PKG_DIR}/usr/share/doc/limoni-voice"

    cp "dist/${ARCH}/limoni-voice" "${PKG_DIR}/usr/bin/limoni-voice"
    chmod 755 "${PKG_DIR}/usr/bin/limoni-voice"
    cp README.md "${PKG_DIR}/usr/share/doc/limoni-voice/README"

    cat <<DESKTOP_EOF > "${PKG_DIR}/usr/share/applications/limoni-voice.desktop"
[Desktop Entry]
Name=Limoni Voice
Comment=P2P Encrypted Voice Chat TUI
Exec=/usr/bin/limoni-voice
Terminal=true
Type=Application
Categories=AudioVideo;Audio;Network;
DESKTOP_EOF

    cat <<CONTROL_EOF > "${PKG_DIR}/DEBIAN/control"
Package: limoni-voice
Version: ${RAW_VERSION}
Section: sound
Priority: optional
Architecture: ${DEB_ARCH}
Maintainer: TheBanri <https://github.com/thebanri>
Description: Terminal-based P2P Encrypted Voice Chat Room built with Limoni TUI
 Limoni Voice is a zero-setup, end-to-end encrypted P2P voice chat application
 featuring real-time audio visualization, noise suppression, and 3D graphics.
CONTROL_EOF

    dpkg-deb --build --root-owner-group "${PKG_DIR}" "release_assets/limoni-voice_${RAW_VERSION}_${DEB_ARCH}.deb"
    rm -rf "${PKG_DIR}"
  fi
}

build_deb "linux-amd64" "amd64"
build_deb "linux-arm64" "arm64"

# 10. Generate Checksums
echo "==> Generating Checksums..."
cd release_assets
sha256sum * > checksums.txt || true
cat checksums.txt

echo "==> Build and Packaging Completed Successfully!"
