#!/usr/bin/env bash
set -e

VERSION="${GITHUB_REF_NAME:-v1.0.0}"
if [ -z "${VERSION}" ] || [ "${VERSION}" = "main" ] || [ "${VERSION}" = "master" ]; then
  VERSION="v1.0.0"
fi
RAW_VERSION="${VERSION#v}"

echo "=========================================="
echo " Building Limoni Voice ${VERSION} (${RAW_VERSION})"
echo "=========================================="

mkdir -p release_assets
mkdir -p dist/linux-amd64 dist/linux-arm64
mkdir -p dist/windows-amd64 dist/windows-arm64
mkdir -p dist/darwin-arm64 dist/darwin-amd64

LDFLAGS="-s -w -X main.AppVersion=${VERSION}"

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

echo "==> Building Windows ARM64 (.exe)..."
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -mod=vendor -ldflags="${LDFLAGS}" -o dist/windows-arm64/limoni-voice.exe .

# 4. Compile macOS (Darwin) Apple Silicon (ARM64) & Intel (AMD64)
echo "==> Building macOS Apple Silicon (ARM64)..."
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -mod=vendor -ldflags="${LDFLAGS}" -o dist/darwin-arm64/limoni-voice .

echo "==> Building macOS Intel (AMD64)..."
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -mod=vendor -ldflags="${LDFLAGS}" -o dist/darwin-amd64/limoni-voice .

# 5. Package Linux Tarballs (.tar.gz)
echo "==> Packaging Linux Tarballs..."
mkdir -p dist/pkg-linux-amd64 dist/pkg-linux-arm64
cp dist/linux-amd64/limoni-voice README.md microphone.obj dist/pkg-linux-amd64/
tar -czf "release_assets/limoni-voice_${VERSION}_linux_amd64.tar.gz" -C dist/pkg-linux-amd64 .

cp dist/linux-arm64/limoni-voice README.md microphone.obj dist/pkg-linux-arm64/
tar -czf "release_assets/limoni-voice_${VERSION}_linux_arm64.tar.gz" -C dist/pkg-linux-arm64 .

# 6. Package macOS Tarballs & Zips (.tar.gz & .zip)
echo "==> Packaging macOS Tarballs..."
mkdir -p dist/pkg-darwin-arm64 dist/pkg-darwin-amd64
cp dist/darwin-arm64/limoni-voice README.md microphone.obj dist/pkg-darwin-arm64/
tar -czf "release_assets/limoni-voice_${VERSION}_darwin_arm64.tar.gz" -C dist/pkg-darwin-arm64 .

cp dist/darwin-amd64/limoni-voice README.md microphone.obj dist/pkg-darwin-amd64/
tar -czf "release_assets/limoni-voice_${VERSION}_darwin_amd64.tar.gz" -C dist/pkg-darwin-amd64 .

if command -v zip >/dev/null 2>&1; then
  zip -j "release_assets/limoni-voice_${VERSION}_darwin_arm64.zip" dist/pkg-darwin-arm64/*
  zip -j "release_assets/limoni-voice_${VERSION}_darwin_amd64.zip" dist/pkg-darwin-amd64/*
fi

# 7. Package Windows Standalone EXEs & Zips
echo "==> Packaging Windows Binaries..."
cp dist/windows-amd64/limoni-voice.exe "release_assets/limoni-voice_${VERSION}_windows_amd64.exe"
cp dist/windows-arm64/limoni-voice.exe "release_assets/limoni-voice_${VERSION}_windows_arm64.exe"

mkdir -p dist/pkg-windows-amd64 dist/pkg-windows-arm64
cp dist/windows-amd64/limoni-voice.exe README.md microphone.obj dist/pkg-windows-amd64/
if command -v zip >/dev/null 2>&1; then
  zip -j "release_assets/limoni-voice_${VERSION}_windows_amd64.zip" dist/pkg-windows-amd64/*
  cp dist/windows-arm64/limoni-voice.exe README.md microphone.obj dist/pkg-windows-arm64/
  zip -j "release_assets/limoni-voice_${VERSION}_windows_arm64.zip" dist/pkg-windows-arm64/*
fi

# 8. Build Debian Packages (.deb)
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

# 9. Generate Checksums
echo "==> Generating Checksums..."
cd release_assets
sha256sum * > checksums.txt || true
cat checksums.txt

echo "==> Build and Packaging Completed Successfully!"
