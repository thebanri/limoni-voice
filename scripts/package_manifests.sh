#!/usr/bin/env bash
# Renders the package manager manifests for one release from its checksums.
#
#   scripts/package_manifests.sh VERSION CHECKSUMS OUT_DIR
#
# VERSION is the release tag (v1.2.3), CHECKSUMS a sha256sum listing of the release assets.
# OUT_DIR receives:
#   homebrew/Formula/limoni-voice.rb     command line (macOS and Linux)
#   homebrew/Casks/limoni-voice-app.rb   Limoni Voice.app (macOS, handles limoni:// links)
#   scoop/limoni-voice.json              Windows; installable straight from its release URL
#   winget/…                             Windows portable package (three manifest files)
#   aur/PKGBUILD, aur/.SRCINFO           Arch Linux limoni-voice-bin
set -euo pipefail

VERSION=$1
CHECKSUMS=$2
OUT=$3
RAW="${VERSION#v}"
REPO="thebanri/limoni-voice"
BASE="https://github.com/${REPO}/releases/download/${VERSION}"
DESC="Terminal-native, end-to-end encrypted P2P voice chat and screen sharing"

sha() {
  local sum
  sum=$(awk -v n="$1" '$2 == n || $2 == "*"n { print $1; exit }' "${CHECKSUMS}")
  if [ -z "${sum}" ]; then
    echo "package_manifests: no checksum for $1" >&2
    exit 1
  fi
  echo "${sum}"
}

MAC_ARM=$(sha "limoni-voice_${VERSION}_darwin_arm64.tar.gz")
MAC_AMD=$(sha "limoni-voice_${VERSION}_darwin_amd64.tar.gz")
LIN_ARM=$(sha "limoni-voice_${VERSION}_linux_arm64.tar.gz")
LIN_AMD=$(sha "limoni-voice_${VERSION}_linux_amd64.tar.gz")
APP_ARM=$(sha "Limoni-Voice_${VERSION}_macOS_arm64.app.zip")
APP_AMD=$(sha "Limoni-Voice_${VERSION}_macOS_amd64.app.zip")
WIN_AMD=$(sha "limoni-voice_${VERSION}_windows_amd64.exe")
WIN_ARM=$(sha "limoni-voice_${VERSION}_windows_arm64.exe")

mkdir -p "${OUT}/homebrew/Formula" "${OUT}/homebrew/Casks" "${OUT}/scoop" "${OUT}/aur" \
  "${OUT}/winget/manifests/t/Thebanri/LimoniVoice/${RAW}"

cat > "${OUT}/homebrew/Formula/limoni-voice.rb" <<EOF
class LimoniVoice < Formula
  desc "${DESC}"
  homepage "https://github.com/${REPO}"
  version "${RAW}"
  license "MIT"

  on_macos do
    on_arm do
      url "${BASE}/limoni-voice_${VERSION}_darwin_arm64.tar.gz"
      sha256 "${MAC_ARM}"
    end
    on_intel do
      url "${BASE}/limoni-voice_${VERSION}_darwin_amd64.tar.gz"
      sha256 "${MAC_AMD}"
    end
  end

  on_linux do
    on_arm do
      url "${BASE}/limoni-voice_${VERSION}_linux_arm64.tar.gz"
      sha256 "${LIN_ARM}"
    end
    on_intel do
      url "${BASE}/limoni-voice_${VERSION}_linux_amd64.tar.gz"
      sha256 "${LIN_AMD}"
    end
  end

  def install
    bin.install "limoni-voice"
  end

  def caveats
    <<~TEXT
      Screen sharing needs ffmpeg and mpv:
        brew install ffmpeg mpv
      On macOS the cask installs Limoni Voice.app instead, which also opens limoni://
      invite links; it includes this command, so use one or the other:
        brew uninstall limoni-voice && brew install --cask thebanri/tap/limoni-voice-app
    TEXT
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/limoni-voice --version")
  end
end
EOF

cat > "${OUT}/homebrew/Casks/limoni-voice-app.rb" <<EOF
cask "limoni-voice-app" do
  arch arm: "arm64", intel: "amd64"

  version "${RAW}"
  sha256 arm:   "${APP_ARM}",
         intel: "${APP_AMD}"

  url "https://github.com/${REPO}/releases/download/v#{version}/Limoni-Voice_v#{version}_macOS_#{arch}.app.zip"
  name "Limoni Voice"
  desc "${DESC}"
  homepage "https://github.com/${REPO}"

  # The app replaces itself when a new release is out.
  auto_updates true
  depends_on macos: ">= :monterey"

  # Installs the same command as the limoni-voice formula: use one or the other.
  app "Limoni Voice.app"
  binary "#{appdir}/Limoni Voice.app/Contents/MacOS/limoni-voice"

  zap trash: [
    "~/Library/Application Support/limoni-voice",
    "~/Library/Caches/limoni-voice",
    "~/Library/Logs/limoni-voice",
  ]
end
EOF

cat > "${OUT}/scoop/limoni-voice.json" <<EOF
{
    "version": "${RAW}",
    "description": "${DESC}",
    "homepage": "https://github.com/${REPO}",
    "license": "MIT",
    "suggest": {
        "Screen sharing": ["ffmpeg", "extras/mpv"]
    },
    "architecture": {
        "64bit": {
            "url": "${BASE}/limoni-voice_${VERSION}_windows_amd64.exe#/limoni-voice.exe",
            "hash": "${WIN_AMD}"
        },
        "arm64": {
            "url": "${BASE}/limoni-voice_${VERSION}_windows_arm64.exe#/limoni-voice.exe",
            "hash": "${WIN_ARM}"
        }
    },
    "bin": "limoni-voice.exe",
    "checkver": "github",
    "autoupdate": {
        "architecture": {
            "64bit": {
                "url": "https://github.com/${REPO}/releases/download/v\$version/limoni-voice_v\$version_windows_amd64.exe#/limoni-voice.exe"
            },
            "arm64": {
                "url": "https://github.com/${REPO}/releases/download/v\$version/limoni-voice_v\$version_windows_arm64.exe#/limoni-voice.exe"
            }
        }
    }
}
EOF

WINGET="${OUT}/winget/manifests/t/Thebanri/LimoniVoice/${RAW}"
cat > "${WINGET}/Thebanri.LimoniVoice.yaml" <<EOF
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.version.1.12.0.schema.json

PackageIdentifier: Thebanri.LimoniVoice
PackageVersion: ${RAW}
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.12.0
EOF
cat > "${WINGET}/Thebanri.LimoniVoice.installer.yaml" <<EOF
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.installer.1.12.0.schema.json

PackageIdentifier: Thebanri.LimoniVoice
PackageVersion: ${RAW}
InstallerType: portable
Commands:
  - limoni-voice
Installers:
  - Architecture: x64
    InstallerUrl: ${BASE}/limoni-voice_${VERSION}_windows_amd64.exe
    InstallerSha256: ${WIN_AMD^^}
  - Architecture: arm64
    InstallerUrl: ${BASE}/limoni-voice_${VERSION}_windows_arm64.exe
    InstallerSha256: ${WIN_ARM^^}
ManifestType: installer
ManifestVersion: 1.12.0
EOF
cat > "${WINGET}/Thebanri.LimoniVoice.locale.en-US.yaml" <<EOF
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.defaultLocale.1.12.0.schema.json

PackageIdentifier: Thebanri.LimoniVoice
PackageVersion: ${RAW}
PackageLocale: en-US
Publisher: thebanri
PublisherUrl: https://github.com/thebanri
PackageName: Limoni Voice
PackageUrl: https://github.com/${REPO}
License: MIT
LicenseUrl: https://github.com/${REPO}/blob/main/LICENSE
ShortDescription: ${DESC}
Tags:
  - voice-chat
  - p2p
  - e2ee
  - terminal
  - screen-sharing
ManifestType: defaultLocale
ManifestVersion: 1.12.0
EOF

cat > "${OUT}/aur/PKGBUILD" <<EOF
# Maintainer: thebanri <https://github.com/thebanri>
pkgname=limoni-voice-bin
pkgver=${RAW}
pkgrel=1
pkgdesc="${DESC}"
arch=('x86_64' 'aarch64')
url="https://github.com/${REPO}"
license=('MIT')
provides=('limoni-voice')
conflicts=('limoni-voice')
optdepends=('ffmpeg: sharing your screen'
            'mpv: watching shared screens')
source_x86_64=("limoni-voice-\${pkgver}-x86_64.tar.gz::https://github.com/${REPO}/releases/download/v\${pkgver}/limoni-voice_v\${pkgver}_linux_amd64.tar.gz")
source_aarch64=("limoni-voice-\${pkgver}-aarch64.tar.gz::https://github.com/${REPO}/releases/download/v\${pkgver}/limoni-voice_v\${pkgver}_linux_arm64.tar.gz")
sha256sums_x86_64=('${LIN_AMD}')
sha256sums_aarch64=('${LIN_ARM}')

package() {
  install -Dm755 limoni-voice "\${pkgdir}/usr/bin/limoni-voice"
  install -Dm644 LICENSE "\${pkgdir}/usr/share/licenses/\${pkgname}/LICENSE"
  install -Dm644 README.md "\${pkgdir}/usr/share/doc/\${pkgname}/README.md"
  # Opens limoni://join/<room key> invite links in a terminal.
  install -Dm644 /dev/stdin "\${pkgdir}/usr/share/applications/limoni-voice.desktop" <<DESKTOP
[Desktop Entry]
Name=Limoni Voice
Comment=${DESC}
Exec=/usr/bin/limoni-voice %u
Terminal=true
Type=Application
Categories=Network;AudioVideo;Chat;
MimeType=x-scheme-handler/limoni;
DESKTOP
}
EOF

cat > "${OUT}/aur/.SRCINFO" <<EOF
pkgbase = limoni-voice-bin
	pkgdesc = ${DESC}
	pkgver = ${RAW}
	pkgrel = 1
	url = https://github.com/${REPO}
	arch = x86_64
	arch = aarch64
	license = MIT
	optdepends = ffmpeg: sharing your screen
	optdepends = mpv: watching shared screens
	provides = limoni-voice
	conflicts = limoni-voice
	source_x86_64 = limoni-voice-${RAW}-x86_64.tar.gz::https://github.com/${REPO}/releases/download/v${RAW}/limoni-voice_v${RAW}_linux_amd64.tar.gz
	sha256sums_x86_64 = ${LIN_AMD}
	source_aarch64 = limoni-voice-${RAW}-aarch64.tar.gz::https://github.com/${REPO}/releases/download/v${RAW}/limoni-voice_v${RAW}_linux_arm64.tar.gz
	sha256sums_aarch64 = ${LIN_ARM}

pkgname = limoni-voice-bin
EOF

echo "==> Package manifests for ${VERSION} written to ${OUT}"
