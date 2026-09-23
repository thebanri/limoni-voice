#!/usr/bin/env bash
set -e

VERSION="${GITHUB_REF_NAME:-}"
if [ -z "${VERSION}" ] || [ "${VERSION}" = "main" ] || [ "${VERSION}" = "master" ]; then
  VERSION=$(git describe --tags --abbrev=0 2>/dev/null || echo "v1.0.0")
fi
# A pull request builds from a ref named like "2/merge"; keep the version to one path segment
# so the package file names stay valid.
VERSION="${VERSION//[^A-Za-z0-9._-]/-}"
RAW_VERSION="${VERSION#v}"

echo "=========================================="
echo " Building Limoni Voice ${VERSION} (${RAW_VERSION})"
echo "=========================================="

rm -rf release_assets dist
mkdir -p release_assets
mkdir -p dist/linux-amd64 dist/linux-arm64
mkdir -p dist/windows-amd64 dist/windows-arm64
mkdir -p dist/darwin-arm64 dist/darwin-amd64

# Update signing. The public key is embedded in the binaries, which from then on refuse
# unsigned updates, so both halves must be present together, and a tagged release (which
# every installed copy auto-updates to) must be signed.
if { [ -n "${UPDATE_SIGNING_KEY:-}" ] && [ -z "${UPDATE_SIGNING_PUBKEY:-}" ]; } ||
   { [ -z "${UPDATE_SIGNING_KEY:-}" ] && [ -n "${UPDATE_SIGNING_PUBKEY:-}" ]; }; then
  echo "error: set both UPDATE_SIGNING_KEY and UPDATE_SIGNING_PUBKEY, or neither" >&2
  exit 1
fi
if [[ "${GITHUB_REF:-}" == refs/tags/* ]] && [ -z "${UPDATE_SIGNING_KEY:-}" ]; then
  echo "error: refusing to build release ${VERSION} unsigned: add the UPDATE_SIGNING_KEY and UPDATE_SIGNING_PUBKEY repository secrets" >&2
  exit 1
fi

LDFLAGS="-s -w -X main.AppVersion=${VERSION}"
if [ -n "${UPDATE_SIGNING_PUBKEY:-}" ]; then
  # Builds that embed a signing key only accept signed updates (checksums.txt.sig)
  LDFLAGS="${LDFLAGS} -X main.UpdateSigningPublicKey=${UPDATE_SIGNING_PUBKEY}"
fi

# 1. Run Unit Tests
echo "==> Running Unit Tests..."
# -short: CI's test job has already run everything with -race. This step installs ffmpeg
# for packaging, which un-skips the real-time screen share tests, and those miss their
# frame and decode-error budgets on 2-core runners (the cause of every failed build since
# 2026-09-13). They still run locally; see internal/p2p/screen_test.go.
go test -mod=vendor -short ./...

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
# -tags bundle embeds the app binary copied next to the installer source (bundle.go);
# without it the installer downloads the app from the latest release.
trap 'rm -f cmd/installer/limoni-voice.exe' EXIT
cp dist/windows-amd64/limoni-voice.exe cmd/installer/limoni-voice.exe
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=vendor -tags bundle -ldflags="${LDFLAGS}" -o release_assets/Limoni-Voice-Setup_windows_amd64.exe ./cmd/installer
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=vendor -tags bundle -ldflags="${LDFLAGS}" -o release_assets/Limoni-Voice-Setup.exe ./cmd/installer

cp dist/windows-arm64/limoni-voice.exe cmd/installer/limoni-voice.exe
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -mod=vendor -tags bundle -ldflags="${LDFLAGS}" -o release_assets/Limoni-Voice-Setup_windows_arm64.exe ./cmd/installer
rm -f cmd/installer/limoni-voice.exe

# 4. Compile macOS (Darwin) Apple Silicon (ARM64) & Intel (AMD64)
echo "==> Building macOS Apple Silicon (ARM64)..."
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -mod=vendor -ldflags="${LDFLAGS}" -o dist/darwin-arm64/limoni-voice .

echo "==> Building macOS Intel (AMD64)..."
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -mod=vendor -ldflags="${LDFLAGS}" -o dist/darwin-amd64/limoni-voice .

## 5. Package Linux Tarballs (.tar.gz)
echo "==> Packaging Linux Tarballs..."
mkdir -p dist/pkg-linux-amd64 dist/pkg-linux-arm64
cp dist/linux-amd64/limoni-voice README.md LICENSE dist/pkg-linux-amd64/
tar -czf "release_assets/limoni-voice_${VERSION}_linux_amd64.tar.gz" -C dist/pkg-linux-amd64 .

cp dist/linux-arm64/limoni-voice README.md LICENSE dist/pkg-linux-arm64/
tar -czf "release_assets/limoni-voice_${VERSION}_linux_arm64.tar.gz" -C dist/pkg-linux-arm64 .

# 6. Package macOS Native Application Bundles (.app.zip & .app.tar.gz)
#
# The bundle opens Limoni Voice in Terminal. Its executable is the native launcher
# (macos/launcher.swift, compiled by CI on macOS into macos/build/limoni-launcher), which also
# receives limoni:// invite links; a local build without it falls back to a shell script that
# opens the app but cannot receive links.
MAC_LAUNCHER="${MAC_LAUNCHER:-macos/build/limoni-launcher}"

# sign_macos_app seals a bundle. Without a signature Apple Silicon Macs report a downloaded app
# as damaged; an ad-hoc signature turns that into the usual "unidentified developer" prompt.
# With MAC_SIGN_P12 (base64 Developer ID .p12) and MAC_SIGN_P12_PASSWORD the signature is a
# real one, and MAC_NOTARY_API_KEY (App Store Connect API key JSON) also notarizes the app.
sign_macos_app() {
  local APP_DIR=$1
  if ! command -v rcodesign >/dev/null 2>&1; then
    echo "    (rcodesign not installed: ${APP_DIR} stays unsigned)"
    return
  fi
  if [ "$(head -c 2 "${APP_DIR}/Contents/MacOS/limoni-voice-launcher")" = "#!" ]; then
    echo "    (shell launcher: ${APP_DIR} cannot be signed, only a native launcher can)"
    return
  fi
  if [ -n "${MAC_SIGN_P12:-}" ]; then
    local SECRETS
    SECRETS="$(mktemp -d)"
    echo "${MAC_SIGN_P12}" | base64 -d > "${SECRETS}/cert.p12"
    printf '%s' "${MAC_SIGN_P12_PASSWORD:-}" > "${SECRETS}/cert.pass"
    rcodesign sign --p12-file "${SECRETS}/cert.p12" --p12-password-file "${SECRETS}/cert.pass" \
      --code-signature-flags runtime "${APP_DIR}"
    if [ -n "${MAC_NOTARY_API_KEY:-}" ]; then
      printf '%s' "${MAC_NOTARY_API_KEY}" > "${SECRETS}/notary.json"
      rcodesign notary-submit --api-key-file "${SECRETS}/notary.json" --staple "${APP_DIR}"
    fi
    rm -rf "${SECRETS}"
  else
    rcodesign sign "${APP_DIR}"
  fi
  # rcodesign's own verify is unreliable for ad-hoc signatures; CI checks the result with
  # Apple's codesign on a Mac (the mac-verify job).
}

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
  # Only code belongs in Contents/MacOS; anything else there breaks the bundle signature.
  cp README.md LICENSE "${APP_DIR}/Contents/Resources/"
  cp macos/LimoniVoice.icns "${APP_DIR}/Contents/Resources/LimoniVoice.icns"

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
    <key>CFBundleIconFile</key>
    <string>LimoniVoice</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>${RAW_VERSION}</string>
    <key>CFBundleVersion</key>
    <string>${RAW_VERSION}</string>
    <key>CFBundleURLTypes</key>
    <array>
        <dict>
            <key>CFBundleURLName</key>
            <string>Limoni Voice invite</string>
            <key>CFBundleURLSchemes</key>
            <array>
                <string>limoni</string>
            </array>
        </dict>
    </array>
    <key>LSApplicationCategoryType</key>
    <string>public.app-category.social-networking</string>
    <key>LSMinimumSystemVersion</key>
    <string>12.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSAppleEventsUsageDescription</key>
    <string>Limoni Voice opens itself in Terminal, which runs the voice chat.</string>
    <key>NSMicrophoneUsageDescription</key>
    <string>Limoni Voice requires microphone access for real-time P2P encrypted voice chat.</string>
</dict>
</plist>
PLIST_EOF

  echo -n "APPL????" > "${APP_DIR}/Contents/PkgInfo"

  if [ -f "${MAC_LAUNCHER}" ]; then
    cp "${MAC_LAUNCHER}" "${APP_DIR}/Contents/MacOS/limoni-voice-launcher"
  else
    echo "    (no native launcher at ${MAC_LAUNCHER}: using the shell launcher, invite links will not open the app)"
    cat <<'LAUNCHER_EOF' > "${APP_DIR}/Contents/MacOS/limoni-voice-launcher"
#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
osascript - "$DIR/limoni-voice" <<'OSA_EOF'
on run argv
    tell application "Terminal"
        activate
        do script "clear; " & quoted form of (item 1 of argv) & "; exit"
    end tell
end run
OSA_EOF
LAUNCHER_EOF
  fi
  chmod 755 "${APP_DIR}/Contents/MacOS/limoni-voice-launcher"

  sign_macos_app "${APP_DIR}"

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
cp dist/darwin-arm64/limoni-voice README.md LICENSE dist/pkg-darwin-arm64/
tar -czf "release_assets/limoni-voice_${VERSION}_darwin_arm64.tar.gz" -C dist/pkg-darwin-arm64 .

cp dist/darwin-amd64/limoni-voice README.md LICENSE dist/pkg-darwin-amd64/
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
Exec=/usr/bin/limoni-voice %u
Terminal=true
Type=Application
Categories=AudioVideo;Audio;Network;
MimeType=x-scheme-handler/limoni;
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

# 10. Package manager manifests (Homebrew, Scoop, winget, AUR) for this release. The Scoop
# manifest is published as its own asset so "scoop install <its URL>" works without a bucket.
echo "==> Rendering package manager manifests..."
(cd release_assets && sha256sum *) > dist/asset-checksums.txt
bash scripts/package_manifests.sh "${VERSION}" dist/asset-checksums.txt dist/manifests
cp dist/manifests/scoop/limoni-voice.json release_assets/limoni-voice.json
tar -czf "release_assets/limoni-voice_${VERSION}_package-manifests.tar.gz" -C dist/manifests .

# 11. Generate Checksums
echo "==> Generating Checksums..."
cd release_assets
sha256sum * > checksums.txt
cat checksums.txt
cd ..

if [ -n "${UPDATE_SIGNING_KEY:-}" ]; then
  echo "==> Signing checksums.txt..."
  go run -mod=vendor ./cmd/limoni-sign sign release_assets/checksums.txt
  # Catch a key pair that does not match before shipping binaries that would reject every
  # future update.
  go run -mod=vendor ./cmd/limoni-sign verify release_assets/checksums.txt
fi

echo "==> Build and Packaging Completed Successfully!"
