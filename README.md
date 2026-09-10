<p align="center">
  <img src="assets/logo.png" alt="Limoni Voice Logo" width="200" />
</p>

<h1 align="center">🍋 Limoni Voice</h1>

<p align="center">
  <b>Terminal-Native • End-to-End Encrypted • P2P Voice Chat & Screen Sharing</b>
</p>

<p align="center">
  <a href="#-installation"><img src="https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-blue?style=for-the-badge" alt="Platform"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-green?style=for-the-badge" alt="License"></a>
  <a href="#"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go"></a>
  <a href="#"><img src="https://img.shields.io/badge/Encryption-AES--256--GCM-critical?style=for-the-badge&logo=letsencrypt&logoColor=white" alt="Encryption"></a>
</p>

<p align="center">
  <i>A zero-dependency, real-time P2P voice chat application running entirely inside your terminal.<br/>
  Built in Go with the <a href="https://github.com/thebanri/limoni">Limoni TUI Framework</a>.</i>
</p>

<p align="center">
  <b>English</b> • <a href="README.tr.md">Türkçe</a>
</p>

<p align="center">
  <img src="assets/screenshot.gif" alt="Limoni Voice Demo Preview" width="85%" />
</p>

---

## ✨ Features

<table>
<tr>
<td width="50%">

### 🎙️ Voice Communication
- **Full-Mesh P2P**: 4-person rooms with direct peer-to-peer UDP
- **AES-256-GCM Encryption**: All audio and control packets are end-to-end encrypted
- **VAD (Voice Activity Detection)**: Real-time speaking detection with 60ms pre-roll lookback buffer
- **Onset & Consonant Protection**: Spectral crest factor discrimination prevents word onset clipping (e.g. unvoiced 's', 'p', 't')
- **Noise Suppression**: Multi-stage noise filter (OFF / Standard / High) with mechanical keyboard & click rejection
- **Live VU-Meter**: Real-time audio level visualization per participant

</td>
<td width="50%">

### 🖥️ Screen Sharing
- **60 FPS Hardware-Accelerated** screen capture (1080p, ultra-low-latency)
- **Native Platform API Support**:
  - **🪟 Windows**: ✅ **Fully Tested & Working** (Win32 GDI & DWM window capture / FFmpeg gdigrab display capture with monitor and window selector)
  - **🍎 macOS**: ✅ **Fully Tested & Working** (Native ScreenCaptureKit & CoreMedia APIs with system permission integration)
  - **🐧 Linux (GNOME)**: ✅ **Fully Tested & Working** (Direct Mutter PipeWire zero-popup monitor capture & Portal window selector)
  - **🐧 Linux (KDE Plasma)**: ✅ **Fully Tested & Working** (XDG Desktop Portal PipeWire capture)
  - **🐧 Other Linux DEs (Hyprland, Sway, XFCE, etc.)**: ⚠️ *Untested / Experimental* (Fallback to GPU Screen Recorder / FFmpeg)
- **MPV / FFplay**: Ultra-low-latency viewer experience

</td>
</tr>
<tr>
<td width="50%">

### 🌐 Network Architecture
- **LAN Auto-Discovery**: Zero-configuration local network discovery via broadcast packets
- **Internet P2P**: WebSocket relay server for NAT traversal and hole-punching
- **Dynamic Port Hopping**: Automatic UDP endpoint rotation for DPI & censorship resistance
- **Anti-Replay Protection**: Strict timestamp window and sliding sequence cache
- **Relay Server**: Ultra-lightweight Go relay hosted on Railway (~7 MB Docker image)

</td>
<td width="50%">

### 🎨 UI & Aesthetics
- **3D Studio Microphone**: 60 FPS spinning 3D model on Braille Canvas
- **Mouse 3D Rotation**: Interactive control via drag & scroll
- **Animated Modals**: Smooth scaling dialog windows
- **Neon & Cyberpunk Palettes**: Multiple themes (Neon, Cyberpunk, Synthwave, Monokai, Dracula)
- **Live VU-Meter**: Real-time audio waveform and volume visualization
- **Toast Notifications**: Instant status updates

</td>
</tr>
<tr>
<td width="50%">

### 📁 E2EE File & Code Sharing
- **Direct P2P Transfer**: Chunked end-to-end encrypted file & code snippet sharing
- **Safety Quarantine**: Executable file warnings and strict filename sanitization
- **Auto-Save**: Accepted files saved to `Downloads/LimoniTransfers`
- **Integrity Verified**: Automatic SHA-256 checksum verification

</td>
<td width="50%">

### 💬 Chat & Room Security
- **Terminal Chat**: Multi-line messaging, clickable links & slash commands (`/help`, `/clear`)
- **Room Lock & PIN**: 4-digit PIN protection (`/lock <pin>`) and host access control
- **Push-to-Talk (PTT)**: Configurable push-to-talk mode with voice activity detection
- **Per-User Volume**: Independent volume leveling and boost per participant

</td>
</tr>
</table>

---

## 🏗️ Architecture

```
                          ┌─────────────────────────────────┐
                          │   WebSocket Relay Server        │
                          │   (Railway / Docker / Self-Host)│
                          │   NAT Traversal & Hole-Punch    │
                          └──────────┬──────────────────────┘
                                     │ WSS
                     ┌───────────────┼───────────────┐
                     │               │               │
              ┌──────▼──────┐ ┌──────▼──────┐ ┌──────▼──────┐
              │   Peer A    │ │   Peer B    │ │   Peer C    │
              │  (Host)     │ │             │ │             │
              ├─────────────┤ ├─────────────┤ ├─────────────┤
              │ Audio Engine│ │ Audio Engine│ │ Audio Engine│
              │ Screen Share│ │ Screen Share│ │ Screen Share│
              │ TUI Render  │ │ TUI Render  │ │ TUI Render  │
              └──────┬──────┘ └──────┬──────┘ └──────┬──────┘
                     │               │               │
                     └───── UDP P2P Full-Mesh ───────┘
                           (AES-256-GCM Encrypted)
```

### Project Structure

```
limoni-voice/
├── main.go              # Application entry point, event loop & screen router
├── network.go           # P2P mesh networking, encryption, WebSocket relay client
├── audio.go             # Audio engine: capture, playback, VAD, noise suppression
├── ui_lobby.go          # Lobby screen: 3D microphone, inputs, menu
├── ui_room.go           # Room screen: participant cards, VU-meters, event logs
├── dialogs.go           # Modal dialogs: sound test, leave room, exit, screen share
├── microphone3d.go      # 3D studio microphone model (polygon & wireframe fallback)
├── screenshare/         # Screen sharing module (GPU Rec, FFmpeg, MPV)
├── clipboard.go         # Cross-platform clipboard support
├── roomcode.go          # Croc-style room code generator
├── relay-server/        # WebSocket relay server (standalone Go module)
├── scripts/             # Build & packaging scripts
├── release_assets/      # Compiled release assets & installers
├── dist/                # Target distribution packages
└── assets/              # Logo, icons, demo preview
```

---

## 🚀 Installation

### 🐧 Linux & 🍎 macOS 1-Line Install (Recommended)

Install Limoni Voice with a single command in your terminal:

```bash
curl -fsSL https://raw.githubusercontent.com/thebanri/limoni-voice/main/install.sh | bash
```

### 🪟 Windows 1-Line Install (PowerShell)

```powershell
irm https://raw.githubusercontent.com/thebanri/limoni-voice/main/scripts/install-windows.ps1 | iex
```

### Prerequisites (Audio & Screen Sharing)

> [!IMPORTANT]
> - **🪟 Windows**: Voice chat works out of the box with **zero dependencies** (using native Win32 `winmm` audio APIs). For Screen Sharing, **FFmpeg** and **MPV** are required.
> - **🐧 Linux**: Voice chat works out of the box on distributions with standard PulseAudio, PipeWire, or ALSA. For Screen Sharing, **FFmpeg** and **MPV** are required.
> - **🍎 macOS**: Because macOS does not include built-in command-line audio capture tools, installing **FFmpeg** and **MPV** (via Homebrew) is required for both voice chat and screen sharing:
>   ```bash
>   brew install ffmpeg mpv
>   ```
> - **🪟 Windows (winget / choco / scoop)**:
>   ```powershell
>   # via winget
>   winget install -e --id Gyan.FFmpeg
>   winget install -e --id shinchiro.mpv
>
>   # or via Chocolatey
>   choco install ffmpeg mpv
>
>   # or via Scoop
>   scoop install ffmpeg mpv
>   ```
>   *(Note: The `Limoni-Voice-Setup.exe` installer can also configure these automatically).*
> - **🐧 Linux (Debian / Ubuntu / Arch / Fedora)**:
>   ```bash
>   # Debian / Ubuntu
>   sudo apt install ffmpeg mpv
>   # Arch Linux
>   sudo pacman -S ffmpeg mpv
>   # Fedora
>   sudo dnf install ffmpeg mpv
>   ```

### Pre-built Binaries (Manual Download)

Download the latest release package from the [**Releases**](https://github.com/thebanri/limoni-voice/releases) page:

| Platform | Architecture | File |
|----------|--------------|------|
| 🐧 Linux | amd64 | `limoni-voice_v1.4.0_linux_amd64.tar.gz` / `.deb` |
| 🐧 Linux | arm64 | `limoni-voice_v1.4.0_linux_arm64.tar.gz` / `.deb` |
| 🍎 macOS | Apple Silicon | `Limoni-Voice_v1.4.0_macOS_arm64.app.zip` |
| 🍎 macOS | Intel | `Limoni-Voice_v1.4.0_macOS_amd64.app.zip` |
| 🪟 Windows | amd64 | `Limoni-Voice-Setup.exe` |
| 🪟 Windows | arm64 | `Limoni-Voice-Setup_windows_arm64.exe` |

```bash
# Linux / macOS
tar xzf limoni-voice_v1.4.0_linux_amd64.tar.gz
./limoni-voice
```

### Building From Source

```bash
# Requirements: Go 1.24+
git clone https://github.com/thebanri/limoni-voice.git
cd limoni-voice
go build -o limoni-voice .
./limoni-voice
```

### Self-Hosted Relay Server (Docker)

To protect your relay server from unauthorized access and bandwidth abuse, you can set an optional **Authentication Secret / Token (`RELAY_AUTH_TOKEN`)**:

```bash
# Option 1: Using Docker Compose (Recommended)
cp .env.example .env
# Set RELAY_AUTH_TOKEN in .env
docker compose up -d

# Option 2: Using Standard Docker CLI
docker build -t limoni-relay .

# Public / open mode:
docker run -d --name limoni-relay -p 8080:8080 limoni-relay

# OR Token-protected secure mode:
docker run -d --name limoni-relay -p 8080:8080 -e RELAY_AUTH_TOKEN="your_secret_key_123" limoni-relay
```

#### Connecting Clients to Protected Relay:

```bash
# Via command-line argument:
./limoni-voice --relay ws://192.168.1.100:8080/ws --relay-token your_secret_key_123

# OR directly in the URL query string:
./limoni-voice --relay "ws://192.168.1.100:8080/ws?token=your_secret_key_123"

# Alternatively, set via environment variables:
export LIMONI_RELAY_URL="ws://192.168.1.100:8080/ws"
export LIMONI_RELAY_TOKEN="your_secret_key_123"
./limoni-voice
```

#### 🌐 Exposing to the Internet with Cloudflare Tunnel (Zero Port Forwarding)

If you are hosting the relay on your home machine or behind CGNAT, you can expose it securely to your friends over the internet using **Cloudflare Tunnel** without opening any ports on your router:

##### Option 1: Using Docker Compose (Zero Installation Needed)
```bash
# 1. Start the relay server along with Cloudflare Tunnel:
docker compose --profile tunnel up -d

# 2. View your temporary public trycloudflare URL:
docker logs limoni-tunnel
# Look for the generated URL in the logs:
# https://xyz-abc-123.trycloudflare.com

# 3. Connect yourself and friends via WSS:
./limoni-voice --relay wss://xyz-abc-123.trycloudflare.com/ws --relay-token your_secret_key_123
```

##### Option 2: Using the `cloudflared` CLI Directly
```bash
# Start a free instant tunnel to your local relay port:
cloudflared tunnel --url http://localhost:8080
# Use the assigned *.trycloudflare.com URL with 'wss://' scheme in clients.
```

##### Option 3: Using a Named Tunnel with Custom Domain
Add your tunnel token from Cloudflare Zero Trust to `.env` as `CLOUDFLARE_TUNNEL_TOKEN=eyJh...` and run `docker compose --profile tunnel up -d`.

---

### LAN-Only / Offline Mode

To use Limoni Voice on an isolated local network (no internet connection required):

```bash
# Force LAN-only direct peer-to-peer mode
./limoni-voice --lan

# Or via environment variable:
export LIMONI_LAN_ONLY=1
./limoni-voice
```

---

## ⚙️ CLI Flags & Configuration

| Flag | Env Variable | Default | Description |
|------|--------------|---------|-------------|
| `--relay <url>` | `LIMONI_RELAY_URL` | `wss://limoni-voice-production.up.railway.app/ws` | Custom WebSocket relay URL for self-hosted servers |
| `--relay-token <token>` | `LIMONI_RELAY_TOKEN` | `""` | Authentication token for password-protected relay servers |
| `--token <token>` | `LIMONI_RELAY_TOKEN` | `""` | Alias for `--relay-token` |
| `--lan`, `--lan-only` | `LIMONI_LAN_ONLY` | `false` | Force LAN-only offline mode (disables internet relay) |
| `--offline` | `LIMONI_OFFLINE` | `false` | Alias for `--lan` |
| `--peer <ip:port>` | `LIMONI_PEER` | `""` | Direct target peer IP/host for cross-subnet or VPN LAN P2P |
| `--connect <ip:port>` | `LIMONI_PEER` | `""` | Alias for `--peer` |
| `--version` | - | - | Print version information and exit |
| `--help`, `-h` | - | - | Show help message and usage instructions |

---

## 🎮 Usage

### Quick Start

```bash
# 1. Launch the application
./limoni-voice

# 2. Your generated room key will appear (e.g. 9421-azure-wave)
# 3. Press [Enter] to host the room
# 4. Share the room key with your friends!
```

### Lobby Screen Shortcuts

| Key | Action |
|-----|--------|
| `Tab` / `Shift+Tab` | Navigate between input fields |
| `Enter` | Host or join room |
| `C` / `F2` | Copy room key to clipboard |
| `G` / `F3` | Generate a new room key |
| `T` / `F4` | Open microphone sound test panel |
| `Esc` | Exit confirmation |
| `Ctrl+V` | Paste key from clipboard |
| `🖱️ Drag` | Rotate 3D microphone model |
| `🖱️ Scroll` | Zoom in / out |

### Room Screen Shortcuts

| Key | Action |
|-----|--------|
| `M` | 🎙️ Mute / Unmute Microphone |
| `D` | 🔇 Deafen / Undeafen audio |
| `N` | 🔊 Cycle Noise Suppression mode |
| `V` | 🖥️ Start / Stop Screen Sharing |
| `W` | 👁️ Watch Live Stream |
| `C` / `F2` | 📋 Copy Room Code |
| `+` / `-` | 🔉 Adjust Microphone Volume |
| `T` | 🧪 Microphone Test Dialog |
| `Esc` | Leave Room |

---

## 🔐 Security

Limoni Voice is built with security from the ground up:

| Layer | Technology | Details |
|-------|------------|---------|
| **End-to-End Encryption** | AES-256-GCM | All voice, chat, control, and file data packets are encrypted end-to-end |
| **Packet Authentication** | AES-256-GCM AEAD Tag | 128-bit GHASH authentication tag guarantees packet integrity |
| **Key Derivation** | Salted HMAC-SHA256 | Cryptographic master key derived with a unique salt from room code |
| **Anti-Replay Protection** | Timestamp + Deduplication | Strict 30s freshness window and sliding-cache replay prevention |
| **Input Sanitization** | Strict Filename Filter | Path traversal, shell characters, and dangerous files quarantined |
| **Magic Header** | `LVS1` | Protocol versioning and header validation |
| **Transport** | WSS (TLS 1.3) / UDP | Encrypted WebSocket for signaling, direct encrypted UDP for P2P media |

> **No audio or file data is ever stored or inspected by the relay server.** The relay is strictly used for peer discovery and NAT traversal. All audio and data flows directly peer-to-peer over encrypted UDP.

---

## 🛠️ Dependencies

### Runtime (Optional)

For screen sharing capabilities:

| Tool | Platform | Purpose |
|------|----------|---------|
| [FFmpeg](https://ffmpeg.org) | All | Video capture & transcoding |
| [GPU Screen Recorder](https://git.dec05eba.com/gpu-screen-recorder) | Linux | Hardware-accelerated screen capture |
| [MPV](https://mpv.io) | All | Ultra-low-latency video playback |

### Go Modules

| Module | Purpose |
|--------|---------|
| [`github.com/thebanri/limoni`](https://github.com/thebanri/limoni) | TUI framework (terminal, widgets, graphics, animations) |
| [`github.com/gorilla/websocket`](https://github.com/gorilla/websocket) | WebSocket relay client |
| [`golang.org/x/sys`](https://pkg.go.dev/golang.org/x/sys) | Platform-native system calls |

---

## 🧪 Testing

```bash
# Run all tests
go test -v ./...

# Unit tests
go test -v -run TestRoomCode
go test -v -run TestAudioEngine

# Relay server tests
cd relay-server && go test -v ./...
```

---

## 🤝 Contributing

Contributions are always welcome! To get started:

1. **Fork** this repository
2. Create a feature branch (`git checkout -b feature/awesome-feature`)
3. Commit your changes (`git commit -m 'feat: add awesome feature'`)
4. Push to your branch (`git push origin feature/awesome-feature`)
5. Open a **Pull Request**

---

## 📄 License

This project is licensed under the [MIT License](LICENSE).

Third-party dependencies:
- **FFmpeg** — LGPL 2.1+ / GPL 2+
- **MPV** — GPL 2+ / LGPL 2.1+
- **GPU Screen Recorder** — GPL 3

---

<p align="center">
  <sub>
    <b>Limoni Voice</b> 💛 developed by the open source community.<br/>
    Built with ❤️ using the <a href="https://github.com/thebanri/limoni">Limoni TUI Framework</a>.
  </sub>
</p>
