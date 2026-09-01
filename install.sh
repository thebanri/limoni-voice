#!/usr/bin/env bash
# ==============================================================================
#  🍋 Limoni Voice - 1-Line Universal Linux & macOS Installer
#  Usage: curl -fsSL https://raw.githubusercontent.com/thebanri/limoni-voice/main/install.sh | bash
# ==============================================================================

set -e

# ANSI Color Codes
CYAN='\033[0;36m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BOLD='\033[1m'
NC='\033[0m' # No Color

echo -e "${CYAN}==========================================${NC}"
echo -e "${YELLOW}${BOLD}   🍋 Limoni Voice - 1-Click Installer    ${NC}"
echo -e "${CYAN}==========================================${NC}"

REPO="thebanri/limoni-voice"
APP_NAME="limoni-voice"

# 1. Detect Operating System
OS="$(uname -s)"
case "${OS}" in
    Linux*)     OS_TYPE="linux" ;;
    Darwin*)    OS_TYPE="darwin" ;;
    *)
        echo -e "${RED}[x] Unsupported Operating System: ${OS}${NC}"
        echo -e "Please download the Windows installer from: https://github.com/${REPO}/releases"
        exit 1
        ;;
esac

# 2. Detect CPU Architecture
ARCH="$(uname -m)"
case "${ARCH}" in
    x86_64|amd64)   ARCH_TYPE="amd64" ;;
    aarch64|arm64)  ARCH_TYPE="arm64" ;;
    *)
        echo -e "${RED}[x] Unsupported Architecture: ${ARCH}${NC}"
        exit 1
        ;;
esac

echo -e "${CYAN}[*] Platform detected:${NC} ${BOLD}${OS_TYPE} (${ARCH_TYPE})${NC}"

# 3. Find Target Installation Directory
if [ -n "${INSTALL_DIR}" ]; then
    mkdir -p "${INSTALL_DIR}"
elif [ "$(id -u)" = "0" ] || [ -w "/usr/local/bin" ]; then
    INSTALL_DIR="/usr/local/bin"
else
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "${INSTALL_DIR}"
fi

# 4. Fetch Latest Release Version Tag
echo -e "${CYAN}[*] Fetching latest release info from GitHub...${NC}"
LATEST_TAG=""
if command -v curl >/dev/null 2>&1; then
    LATEST_TAG=$(curl -sSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name":' | head -n 1 | sed -E 's/.*"([^"]+)".*/\1/')
fi

if [ -z "${LATEST_TAG}" ]; then
    # Fallback to redirect resolution if API is rate-limited
    LATEST_TAG=$(curl -sSI "https://github.com/${REPO}/releases/latest" 2>/dev/null | grep -i "location:" | head -n 1 | sed -E 's|.*/tag/([^/\r\n]+).*|\1|')
fi

if [ -z "${LATEST_TAG}" ]; then
    LATEST_TAG="v1.4.4"
fi

echo -e "${GREEN}[+] Target version:${NC} ${BOLD}${LATEST_TAG}${NC}"

# 5. Build Download URL
TARBALL_NAME="${APP_NAME}_${LATEST_TAG}_${OS_TYPE}_${ARCH_TYPE}.tar.gz"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${LATEST_TAG}/${TARBALL_NAME}"

# 6. Download and Extract
TMP_DIR=$(mktemp -d)
cleanup() {
    rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

echo -e "${CYAN}[*] Downloading ${TARBALL_NAME}...${NC}"
if ! curl -fsSL "${DOWNLOAD_URL}" -o "${TMP_DIR}/${TARBALL_NAME}"; then
    echo -e "${RED}[x] Failed to download package from: ${DOWNLOAD_URL}${NC}"
    exit 1
fi

echo -e "${CYAN}[*] Extracting package...${NC}"
tar -xzf "${TMP_DIR}/${TARBALL_NAME}" -C "${TMP_DIR}"

if [ ! -f "${TMP_DIR}/${APP_NAME}" ]; then
    echo -e "${RED}[x] Binary ${APP_NAME} not found in archive!${NC}"
    exit 1
fi

# 7. Install Binary
echo -e "${CYAN}[*] Installing ${APP_NAME} to ${INSTALL_DIR}...${NC}"
mv "${TMP_DIR}/${APP_NAME}" "${INSTALL_DIR}/${APP_NAME}"
chmod +x "${INSTALL_DIR}/${APP_NAME}"

# 8. Setup Assets (3D model & App Icon)
ASSETS_DIR="${HOME}/.limoni-voice"
mkdir -p "${ASSETS_DIR}"
if [ -f "${TMP_DIR}/microphone.obj" ]; then
    cp "${TMP_DIR}/microphone.obj" "${ASSETS_DIR}/microphone.obj" 2>/dev/null || true
else
    curl -sSL "https://raw.githubusercontent.com/${REPO}/main/microphone.obj" -o "${ASSETS_DIR}/microphone.obj" 2>/dev/null || true
fi

# 9. Create Desktop Entry & Icon on Linux
if [ "${OS_TYPE}" = "linux" ]; then
    ICON_DIR="${HOME}/.local/share/icons/hicolor/256x256/apps"
    mkdir -p "${ICON_DIR}"
    curl -sSL "https://raw.githubusercontent.com/${REPO}/main/assets/logo.png" -o "${ICON_DIR}/limoni-voice.png" 2>/dev/null || true

    DESKTOP_DIR="${HOME}/.local/share/applications"
    mkdir -p "${DESKTOP_DIR}"
    cat <<EOF > "${DESKTOP_DIR}/limoni-voice.desktop"
[Desktop Entry]
Name=Limoni Voice
Comment=Terminal P2P Voice Chat & Screen Sharing
Exec=${INSTALL_DIR}/${APP_NAME}
Icon=limoni-voice
Terminal=true
Type=Application
Categories=Network;AudioVideo;Chat;
Keywords=voice;chat;p2p;terminal;limoni;
EOF
    chmod +x "${DESKTOP_DIR}/limoni-voice.desktop" 2>/dev/null || true
fi

# 10. Ensure PATH includes INSTALL_DIR
SHELL_UPDATED=false
if [[ ":$PATH:" != *":${INSTALL_DIR}:"* ]]; then
    EXPORT_LINE="export PATH=\"${INSTALL_DIR}:\$PATH\""
    for RC_FILE in "${HOME}/.bashrc" "${HOME}/.zshrc" "${HOME}/.profile"; do
        if [ -f "${RC_FILE}" ] && ! grep -q "${INSTALL_DIR}" "${RC_FILE}"; then
            echo "" >> "${RC_FILE}"
            echo "# Added by Limoni Voice installer" >> "${RC_FILE}"
            echo "${EXPORT_LINE}" >> "${RC_FILE}"
            SHELL_UPDATED=true
        fi
    done
fi

# 11. Check Optional Screen Sharing Tools (FFmpeg & MPV)
DEPS_HINT=""
HAS_MPV=false
HAS_FFMPEG=false
command -v mpv >/dev/null 2>&1 && HAS_MPV=true
command -v ffmpeg >/dev/null 2>&1 && HAS_FFMPEG=true

if [ "${HAS_MPV}" = false ] || [ "${HAS_FFMPEG}" = false ]; then
    DEPS_HINT="${YELLOW}💡 Tip for Screen Sharing:${NC} To broadcast or watch live screen streams, install MPV & FFmpeg:\n"
    if [ "${OS_TYPE}" = "linux" ]; then
        if command -v apt >/dev/null 2>&1; then
            DEPS_HINT+="   ${BOLD}sudo apt install ffmpeg mpv${NC}\n"
        elif command -v pacman >/dev/null 2>&1; then
            DEPS_HINT+="   ${BOLD}sudo pacman -S ffmpeg mpv${NC}\n"
        elif command -v dnf >/dev/null 2>&1; then
            DEPS_HINT+="   ${BOLD}sudo dnf install ffmpeg mpv${NC}\n"
        fi
    elif [ "${OS_TYPE}" = "darwin" ]; then
        DEPS_HINT+="   ${BOLD}brew install ffmpeg mpv${NC}\n"
    fi
fi

# 12. Completion Message
echo ""
echo -e "${GREEN}==========================================${NC}"
echo -e "${GREEN}${BOLD}  ✅ Limoni Voice ${LATEST_TAG} installed!   ${NC}"
echo -e "${GREEN}==========================================${NC}"
echo -e "📍 Installed to: ${BOLD}${INSTALL_DIR}/${APP_NAME}${NC}"
echo -e "🚀 Run '${BOLD}${APP_NAME}${NC}' in your terminal to start."
echo ""
if [ "${SHELL_UPDATED}" = true ]; then
    echo -e "${YELLOW}[!] PATH updated. If '${APP_NAME}' command is not recognized, restart terminal or run:${NC}"
    echo -e "    ${BOLD}source ~/.bashrc${NC} (or ~/.zshrc)\n"
fi
if [ -n "${DEPS_HINT}" ]; then
    echo -e "${DEPS_HINT}"
fi
