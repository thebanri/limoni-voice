<p align="center">
  <img src="assets/logo.png" alt="Limoni Voice Logo" width="200" />
</p>

<h1 align="center">🍋 Limoni Voice</h1>

<p align="center">
  <b>Terminal-Native • End-to-End Encrypted • P2P Voice Chat & Screen Sharing</b>
</p>

<p align="center">
  <a href="#-installation"><img src="https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-blue?style=for-the-badge" alt="Platform"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-AGPL--3.0-blue?style=for-the-badge" alt="License"></a>
  <a href="#"><img src="https://img.shields.io/badge/Go-1.26+-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go"></a>
  <a href="#"><img src="https://img.shields.io/badge/Encryption-AES--256--GCM-critical?style=for-the-badge&logo=letsencrypt&logoColor=white" alt="Encryption"></a>
</p>

<p align="center">
  <i>A pure-Go (no cgo), single-binary, real-time P2P voice chat application running entirely inside your terminal.<br/>
  Built in Go with the <a href="https://github.com/thebanri/limoni">Limoni TUI Framework</a>.</i>
</p>

<p align="center">
  <b>English</b> • <a href="README.tr.md">Türkçe</a>
</p>

<p align="center">
  <img src="https://raw.githubusercontent.com/thebanri/limoni-voice/media/screenshot.gif" alt="Limoni Voice Demo Preview" width="85%" />
</p>

---

## ✨ Features

<table>
<tr>
<td width="50%">

### 🎙️ Voice Communication
- **Opus 48 kHz**: CBR with in-band FEC; FEC redundancy follows the receivers' packet loss, and the bitrate steps between 12 and 32 kbps with the worst link in the room (drops quickly on loss or high latency, climbs back after 10 s of clean reports)
- **Adaptive Jitter Buffer**: RFC 3550 jitter estimation, packet-loss concealment and FEC recovery
- **Echo Cancellation (AEC)**: Pure-Go Speex MDF port — use speakers without headphones
- **Noise & Click Suppression**: Multi-band filter or pure-Go RNNoise (OFF / Standard / High / AI) plus a look-ahead transient suppressor for keyboard clicks and claps
- **Voice Smoothing**: De-harsh EQ, soft compressor and limiter for even, non-fatiguing loudness
- **VAD**: Real-time speaking detection with pre-roll lookback buffer and onset protection
- **Global Push-to-Talk**: Works while the terminal is unfocused (X11, XDG portal on Wayland, Win32, macOS)
- **Native Audio I/O**: PulseAudio/PipeWire protocol, CoreAudio, winmm — no external tools needed

</td>
<td width="50%">

### 🖥️ Screen Sharing
- **60 FPS Hardware-Accelerated** screen capture (1080p, ultra-low-latency)
- **Native Platform API Support**:
  - **🪟 Windows**: ✅ **Fully Tested & Working** (Win32 GDI & DWM window capture / FFmpeg gdigrab display capture with monitor and window selector)
  - **🍎 macOS**: ✅ **Fully Tested & Working** (Native ScreenCaptureKit capture of any display or window, VideoToolbox hardware encoding with an x264 fallback, and a clear message when a macOS permission is missing)
  - **🐧 Linux (GNOME)**: ✅ **Fully Tested & Working** (Direct Mutter PipeWire zero-popup monitor capture & Portal window selector)
  - **🐧 Linux (KDE Plasma)**: ✅ **Fully Tested & Working** (XDG Desktop Portal PipeWire capture)
  - **🐧 Other Linux DEs (Hyprland, Sway, XFCE, etc.)**: ⚠️ *Untested / Experimental* (Fallback to GPU Screen Recorder / FFmpeg)
- **Watcher-Only Delivery**: Video is uploaded only to members who actually watch (no bandwidth used otherwise)
- **Quality Presets & Adaptive Bitrate**: 720p30 (default, 2.5 Mbps) up to 1080p120; the stream steps down automatically when viewers lose packets
- **Loss Recovery**: NACK retransmission, time-based reorder buffer and paced sending; new viewers start instantly from the last keyframe
- **System Audio**: Share what your computer plays (PipeWire/PulseAudio monitor, WASAPI loopback, ScreenCaptureKit) with your own voice chat removed
- **MPV / FFplay**: Ultra-low-latency viewer experience (stream fed through a private pipe)

</td>
</tr>
<tr>
<td width="50%">

### 🌐 Network Architecture
- **LAN Auto-Discovery**: Zero-configuration local network discovery via broadcast packets
- **Path Ladder**: LAN → direct P2P (IPv6 / IPv4 hole-punch) → UDP relay → WebSocket relay
- **NAT Classification**: STUN-based cone/symmetric detection with port spraying & multi-socket punching
- **UDP Relay**: Low-latency encrypted media relay when direct paths fail (symmetric NAT, CGNAT)
- **Redundant Audio**: Audio is sent over both relay and direct paths, receiver deduplicates
- **Live Diagnostics**: Per-peer path, RTT, loss and jitter in the debug panel (`F12`) and `/net`
- **Relay Server**: Lightweight Go relay with Prometheus `/metrics` (including idle-room and kick/ban counters), structured logs and auth tokens that rotate without a restart

</td>
<td width="50%">

### 🎨 UI & Aesthetics
- **3D Studio Microphone**: 60 FPS spinning 3D model on Braille Canvas
- **Mouse 3D Rotation**: Interactive control via drag & scroll
- **Animated Modals**: Smooth scaling dialog windows
- **Neon & Cyberpunk Palettes**: Multiple themes (Neon, Cyberpunk, Synthwave, Monokai, Dracula)
- **Live VU-Meter**: Real-time audio waveform and volume visualization
- **Toast Notifications**: Instant status updates
- **English & Türkçe**: Switch the interface language with `L` in the lobby, `I` in the audio settings, `--lang tr` or `LIMONI_LANG=tr`

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
- **Desktop Notifications**: Chat messages, joins and file offers raise a system notification while the terminal is in the background (toggle with `B` in settings)
- **Invite Links**: `/invite` copies a `limoni://join/<key>` link; on Linux, Windows and macOS (Limoni Voice.app) installs it opens Limoni Voice with the room filled in (press Enter to join). `limoni-voice --join <key or link>` joins directly
- **Knock to Join**: `/knock` makes everyone who has the room key wait until the host lets them in (`Y`) or turns them away (`N`)
- **Room Lock & PIN**: 4-digit PIN protection (`/lock <pin>`) and host access control
- **Kick & Ban**: The host removes a member with `/kick <user>` or keeps them out with `/ban <user>` (for as long as the room is open). The removal is proven with the host's key, so no member can fake one, and the group key is rotated at once so the removed member cannot follow the room
- **Push-to-Talk (PTT)**: Configurable push-to-talk mode with voice activity detection
- **Per-User Volume**: Independent volume leveling and boost per participant

</td>
</tr>
</table>

---

## 🏗️ Architecture

```
      ┌──────────────────────────────────────────────────────────┐
      │ Relay server (Docker / VPS / Cloudflare Tunnel)          │
      │ WSS: signaling, PAKE handshake, fallback media           │
      │ UDP: low-latency encrypted media relay                   │
      └───────┬─────────────────────┬─────────────────────┬──────┘
              │                     │                     │
              ▼                     ▼                     ▼
      ┌──────────────┐      ┌──────────────┐      ┌──────────────┐
      │ Peer A (Host)│      │ Peer B       │      │ Peer C       │
      ├──────────────┤      ├──────────────┤      ├──────────────┤
      │ Opus + FEC   │      │ Opus + FEC   │      │ Opus + FEC   │
      │ AEC · RNNoise│      │ AEC · RNNoise│      │ AEC · RNNoise│
      │ Limoni TUI   │      │ Limoni TUI   │      │ Limoni TUI   │
      └───────┬──────┘      └───────┬──────┘      └───────┬──────┘
              └─────────────────────┴─────────────────────┘
                direct UDP / IPv6 hole-punch (full mesh)
                (group key AES-256-GCM, rotated on join/leave)
```

### Project Structure

```
limoni-voice/
├── main.go              # Bootstrap: flags, language, log file, backend, node start
├── app*.go              # App state, key/mouse routing, render loop, settings, kick/knock UI
├── ui_*.go              # Lobby and room screens: cards, HUD, chat, controls
├── dialogs.go           # Modals: audio settings, relay, debug/diagnostics, share, file offers
├── i18n.go              # T / Tf / tr helpers over internal/i18n
├── internal/
│   ├── p2p/             # P2P node: relay, handshake, NAT, media, files, screen share, kick/ban, stats
│   ├── engine/          # Audio engine: 48 kHz capture/playback, VAD, AEC, denoise, mixing, SFX
│   ├── i18n/            # Interface translations (English source, Turkish catalog)
│   ├── applog/          # Rotating diagnostic log in the per-user state directory
│   ├── protocol/        # Binary packet format + relay signaling types
│   ├── e2ee/            # Room codes, CPace PAKE handshake, epoch keyring, member proofs
│   ├── relay/           # Relay server (WebSocket + UDP), kick/ban, token rotation, metrics
│   ├── nat/             # STUN NAT classification, punch strategies, IPv6
│   ├── voice/           # Opus codec wrapper, adaptive bitrate controller, jitter buffer
│   ├── dsp/             # Speex MDF echo canceller, RNNoise, FFT
│   ├── audioio/         # PulseAudio / CoreAudio / winmm / tool fallback
│   ├── sysaudio/        # System audio capture for screen share
│   ├── video/           # Screen share video transport (reorder, pacing, NACK)
│   ├── notify/          # Desktop notifications
│   └── ptt/             # System-wide push-to-talk (X11, portal, Win32, macOS)
├── screenshare/         # Screen sharing module (GPU Rec, FFmpeg, MPV)
├── relay-server/        # Relay binary (thin wrapper over internal/relay)
├── cmd/limoni-sign/     # ed25519 signing tool for release checksums
└── scripts/             # Build & packaging scripts
```

---

## 🚀 Installation

### 🐧 Linux & 🍎 macOS 1-Line Install (Recommended)

Install Limoni Voice with a single command in your terminal:

```bash
curl -fsSL https://raw.githubusercontent.com/thebanri/limoni-voice/main/install.sh | bash
```

Or with [Homebrew](https://brew.sh) (macOS and Linux, updates with `brew upgrade`):

```bash
brew install thebanri/tap/limoni-voice              # the command
brew install --cask thebanri/tap/limoni-voice-app   # macOS: Limoni Voice.app, opens invite links
```

Use one or the other: the cask includes the command.

### 🪟 Windows 1-Line Install (PowerShell)

```powershell
irm https://raw.githubusercontent.com/thebanri/limoni-voice/main/scripts/install-windows.ps1 | iex
```

Or with [Scoop](https://scoop.sh) (updates with `scoop update limoni-voice`):

```powershell
scoop install https://github.com/thebanri/limoni-voice/releases/latest/download/limoni-voice.json
```

### Prerequisites (Audio & Screen Sharing)

> [!IMPORTANT]
> - **🪟 Windows**: Voice chat works out of the box with **zero dependencies** (using native Win32 `winmm` audio APIs). For Screen Sharing, **FFmpeg** and **MPV** are required.
> - **🐧 Linux**: Voice chat works out of the box on distributions with standard PulseAudio, PipeWire, or ALSA. For Screen Sharing, **FFmpeg** and **MPV** are required.
> - **🍎 macOS**: Requires macOS 12 Monterey or later. Voice chat uses native CoreAudio (no dependencies). For screen sharing, install **FFmpeg** and **MPV** via Homebrew:
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
# Requirements: Go 1.26+
git clone https://github.com/thebanri/limoni-voice.git
cd limoni-voice
go build -o limoni-voice .
./limoni-voice
```

### Self-Hosted Relay Server (Docker)

The relay server defaults to port **27850** (avoiding collision with common ports like 8080 and deterring port scanners). You can customize `PORT` in `.env` to any port you prefer.

To protect your relay server from unauthorized access and bandwidth abuse, you can set an optional **Authentication Secret / Token (`RELAY_AUTH_TOKEN`)**:

```bash
# Option 1: Using Docker Compose (Recommended)
cp .env.example .env
# Set PORT and RELAY_AUTH_TOKEN in .env
docker compose up -d

# Option 2: Using Standard Docker CLI
docker build -t limoni-relay .
# Token-protected secure mode (Default Port 27850):
docker run -d --name limoni-relay -p 27850:27850 -e RELAY_AUTH_TOKEN="your_secret_key_123" limoni-relay
```

#### Connecting Clients to Protected Relay:

##### 1. Via In-App Graphical Modal (Recommended - Friendly for Everyone):
To configure without messing with command-line arguments:
- In the lobby, press **`[R]`** or click the **`[ R : Relay & Security Settings ]`** footer button.
- From any screen, press the **`F5`** shortcut.
- In room chat, type **`/relay`** or **`/server`**.

In the animated dialog, configure the **WebSocket URL** and optional **Server Password / Token**, then click **`[ Save & Connect ]`**. Settings are persisted across sessions (`settings.json`).

##### 2. Via Command-Line Flags or Environment Variables:
```bash
# Via command-line argument:
./limoni-voice --relay ws://192.168.1.100:27850/ws --relay-token your_secret_key_123

# OR directly in the URL query string:
./limoni-voice --relay "ws://192.168.1.100:27850/ws?token=your_secret_key_123"

# Alternatively, set via environment variables:
export LIMONI_RELAY_URL="ws://192.168.1.100:27850/ws"
export LIMONI_RELAY_TOKEN="your_secret_key_123"
./limoni-voice
```

#### 🌐 Exposing to the Internet with Cloudflare Tunnel (Zero Port Forwarding)

If you are hosting the relay on your home machine or behind CGNAT, you can expose it securely to your friends over the internet using **Cloudflare Tunnel** without opening any ports on your router:

##### 🚀 Option 1: Instant Quick Tunnel (NO Token or Cloudflare Account Needed)
Get a free, instant HTTPS/WSS URL without registering or configuring anything:
```bash
# 1. Start relay server and quick tunnel with a single command:
docker compose --profile quick-tunnel up -d

# 2. View your temporary public trycloudflare URL:
docker logs limoni-quick-tunnel
# Look for the generated URL in the logs:
# https://funny-animal-1234.trycloudflare.com

# 3. Connect yourself and friends via WSS:
./limoni-voice --relay wss://funny-animal-1234.trycloudflare.com/ws --relay-token your_secret_key_123
```

##### 🔑 Option 2: Using a Named Tunnel with Custom Domain (Token)
Add your tunnel token from Cloudflare Zero Trust to `.env` as `CLOUDFLARE_TUNNEL_TOKEN=eyJh...` and run:
```bash
docker compose --profile tunnel up -d
```

##### 💻 Option 3: Using the `cloudflared` CLI Directly
```bash
cloudflared tunnel --url http://localhost:27850
```

##### 🛑 Stopping Relay and Tunnels
Since tunnel services are defined under Docker Compose profiles, running a simple `docker compose down` will only stop the relay service. To stop everything (including all active tunnels):

```bash
# Stop all services and all tunnels across all profiles:
docker compose --profile "*" down

# Or stop a specific active profile:
docker compose --profile quick-tunnel down

# Remove any orphaned/leftover background containers:
docker compose down --remove-orphans
```

> [!TIP]
> **Network / UDP QUIC Issues:** `docker-compose.yml` is pre-configured with `--protocol http2` and public DNS to route tunnel traffic over standard HTTPS (TCP 443) instead of UDP port 7844 (QUIC), preventing "sendmsg: network is unreachable" errors caused by ISP UDP drops.

#### 🔁 Backup Relays

Give several relays separated by commas, in the relay dialog (`R`), in `--relay` or in `LIMONI_RELAY_URL`:

```bash
./limoni-voice --relay "my.relay.com, backup.relay.com:27850"
```

The first one is the primary. A host whose primary is unreachable opens the room on the next one; a joiner that cannot reach a relay, or does not find the room there, asks the next one. Once a room is open it stays on its relay. Everyone in a room should list the same relays in the same order. A relay with its own password takes it in the URL: `wss://backup.relay.com/ws?token=secret`.

#### ⚡ Low Latency: UDP Relay (Important for Cloudflare Tunnel users)

Cloudflare Tunnel only carries **TCP/WebSocket**. When two peers cannot connect directly (e.g. symmetric NAT or CGNAT), audio falls back to the WebSocket relay inside the tunnel, which typically adds **~100+ ms**. For the lowest latency:

1. Run the relay on a VPS, or port-forward **UDP 27850** on your router.
2. Tell clients where the UDP relay is reachable:
   ```bash
   RELAY_UDP_PUBLIC_ADDR=203.0.113.10:27850 docker compose --profile tunnel up -d
   ```
3. Check the debug panel (`F12`) or type `/net` in chat: the peer path should show `P2P`, `P2P-v6` or `Relay-UDP` instead of `Relay`.

| Env Variable | Default | Description |
|--------------|---------|-------------|
| `PORT` | `27850` | TCP port for HTTP/WebSocket |
| `UDP_PORT` | same as `PORT` | UDP relay port (`0` / `off` disables it) |
| `RELAY_UDP_PUBLIC_ADDR` | – | `host:port` advertised for UDP (required behind a tunnel) |
| `RELAY_AUTH_TOKEN` | – | Optional shared secret for clients; several can be given separated by commas (e.g. the old and the new one while rotating) |
| `RELAY_AUTH_TOKEN_FILE` | – | File with one accepted secret per line (`#` comments). Re-read on `SIGHUP` (`docker kill -s HUP limoni-relay`) and whenever it changes, so secrets rotate without a restart; open connections stay up |
| `TRUST_PROXY` | `false` | Always trust `CF-Connecting-IP` / `X-Forwarded-For` (private-network proxies are trusted automatically) |
| `LOG_FORMAT` / `LOG_LEVEL` | `json` / `info` | Structured logging options |

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
| `--relay <url>` | `LIMONI_RELAY_URL` | `wss://relay.thebanri.dpdns.org/ws` | Custom WebSocket relay URL for self-hosted servers; backups after commas (see below) |
| `--relay-token <token>` | `LIMONI_RELAY_TOKEN` | `""` | Authentication token for password-protected relay servers |
| `--token <token>` | `LIMONI_RELAY_TOKEN` | `""` | Alias for `--relay-token` |
| `--lan`, `--lan-only` | `LIMONI_LAN_ONLY` | `false` | Force LAN-only offline mode (disables internet relay) |
| `--offline` | `LIMONI_OFFLINE` | `false` | Alias for `--lan` |
| `--peer <ip:port>` | `LIMONI_PEER` | `""` | Direct target peer IP/host for cross-subnet or VPN LAN P2P |
| `--connect <ip:port>` | `LIMONI_PEER` | `""` | Alias for `--peer` |
| `--sysaudio-test` | - | - | Capture system audio for 5 s, print the level and exit (screen share audio diagnosis) |
| `--version` | - | - | Print version information and exit |
| `--lang <en\|tr>` | `LIMONI_LANG` | saved setting, else `en` | Interface language (English / Türkçe) |
| `--log-file <path>` | `LIMONI_LOG_FILE` | see below | Diagnostic log file |
| `--help`, `-h` | - | - | Show help message and usage instructions |

---

### 🌍 Interface Language

The interface is written in English and ships with a Turkish translation. Press `L` in the lobby or `I` in the audio settings (`T`) to switch; the choice is saved in `settings.json`. `--lang` and `LIMONI_LANG` override it for one run. Log files stay in English so they can be shared in bug reports.

### 📝 Diagnostic Log

The log is written to the per-user state directory instead of the folder the app was started from, and rotates at 5 MB (one `.1` backup is kept):

| Platform | Location |
|----------|----------|
| Linux | `$XDG_STATE_HOME/limoni-voice/limoni-voice.log` (`~/.local/state/limoni-voice/`) |
| macOS | `~/Library/Logs/limoni-voice/limoni-voice.log` |
| Windows | `%LOCALAPPDATA%\limoni-voice\logs\limoni-voice.log` |

### 🖥️ Screen Sharing Tips

- Press **`V`** in a room, pick a **quality preset** (`1`–`5`, or `Q` to cycle) and toggle **system audio** with **`A`**. Missing tools and the install command are shown in the dialog.
- Nothing is uploaded until someone watches (`W` or click the stream). The sharer's bitrate then adapts to the viewers' connection (a short picture hiccup when it changes).
- The debug panel (**`F12`**) shows preset, current bitrate, viewers, uplink queue and retransmissions.
- **Relay operators:** update the relay server to get per-viewer delivery through the relay; older relays still work but forward relayed video to the whole room.
- **System audio not shared?** Run `limoni-voice --sysaudio-test`: it prints the capture backend and a level meter while you play something. On Windows the default playback device is captured (Sound settings → Output), and an idle device delivers nothing.
- **macOS:** release builds embed the capture helper; self-built binaries compile it on first use (`xcode-select --install`). System audio needs macOS 13+. Every display is offered separately, and a shared window is captured from the display it is on. Video is encoded by the VideoToolbox hardware encoder when your FFmpeg has it (Homebrew's does), otherwise by x264.

### 🍎 macOS Notes

- **Install:** the one-line installer puts `Limoni Voice.app` in `/Applications` (or `~/Applications`) and links the `limoni-voice` command to it, so there is one copy to update. The app opens Limoni Voice in Terminal and handles `limoni://` invite links.
- **First launch:** release apps are signed ad-hoc unless the release was built with a Developer ID. If macOS says it cannot verify the developer, Control-click the app → **Open** (on macOS 15: System Settings → Privacy & Security → **Open Anyway**) once.
- **Permissions belong to your terminal** (Terminal, iTerm2, …), because Limoni Voice runs inside it. Allow it under System Settings → Privacy & Security, then restart the terminal:

| Feature | Permission | What happens without it |
|---------|------------|-------------------------|
| Talking | Microphone | Limoni Voice warns at start: others would hear silence |
| Screen sharing, system audio | Screen & System Audio Recording | The share dialog and the room log say what to enable |
| Global push-to-talk | Input Monitoring | The audio settings show the permission to enable |
| Opening from the app | Automation → Terminal | Asked once, the first time the app opens Terminal |

- **Updates:** a copy running from `Limoni Voice.app` updates the whole app bundle (checksum-verified), so its signature stays intact.

## 🎮 Usage

### Quick Start

```bash
# 1. Launch the application
./limoni-voice

# 2. Your generated room key will appear (e.g. 9421-azure-wave-lemon)
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
| `L` | Switch interface language (English / Türkçe) |
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
| `P` | 🎚️ Toggle Voice Activity / Push-to-Talk |
| `E` | 🔁 Toggle echo cancellation |
| `S` | 🪶 Toggle voice smoothing (soft highs, even loudness, peak limiter) |
| `F12` | 🩺 Debug & network diagnostics |
| `/net`, `/stats` | 📶 Print per-peer path, RTT, loss & jitter to chat |
| `/kick <user>` | 👢 Remove a member (host only; they may knock again) |
| `/ban <user>` | ⛔ Remove a member and keep their ID and address out while the room is open (host only) |
| `Esc` | Leave Room |

---

## 🔐 Security

| Layer | Technology | Details |
|-------|------------|---------|
| **Room Code** | `NNNN-word-word-word` | The 4 digits are the public room ID the relay sees; the 3 words are the secret and never leave your machine |
| **Authentication** | CPace PAKE (ristretto255) | Joiners prove they know the code without revealing it; offline guessing from captured traffic is impossible |
| **Group Key** | Random AES-256 epoch key | Generated by the host and delivered over the PAKE channel; rotated when a member joins or leaves, the host changes or ports hop |
| **Rekeys** | X25519 member keys | Every member has a session key vouched for by the host inside the PAKE channel. Each new group key is sealed separately per member, so a departed member cannot read it and no member can forge one; a newly elected host rekeys the same way |
| **Encryption** | AES-256-GCM | Voice, chat, control, video and file packets are encrypted end-to-end with random nonces |
| **Anti-Replay** | Sequence + timestamp window | Freshness window and sliding deduplication cache |
| **Host Approval** | Room lock & PIN | The host can lock the room and require a 4-digit PIN checked on the host side |
| **Removal** | Host-proven kick / ban | A removal carries pairwise tags only the host can compute; the group key rotates immediately without the removed member, and a missed rotation is recovered by asking the host for the key |
| **Updates** | SHA-256 + ed25519 | Self-update refuses assets without a matching `checksums.txt`; signed checksums are verified when a public key is embedded |
| **Transport** | WSS (TLS) / UDP | Signaling over WebSocket, media over direct or relayed encrypted UDP |

> **The relay cannot decrypt anything.** It only sees the numeric room ID, opaque PAKE messages and encrypted media frames. When a direct path is not possible, encrypted audio is forwarded through the relay (UDP when available, otherwise WebSocket).

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
| [`github.com/gorilla/websocket`](https://github.com/gorilla/websocket) | WebSocket relay transport |
| [`github.com/thesyncim/gopus`](https://github.com/thesyncim/gopus) | Pure-Go Opus codec |
| [`filippo.io/cpace`](https://pkg.go.dev/filippo.io/cpace) | CPace password-authenticated key exchange |
| [`github.com/jfreymuth/pulse`](https://github.com/jfreymuth/pulse) | Native PulseAudio/PipeWire client |
| [`github.com/ebitengine/purego`](https://github.com/ebitengine/purego) | CoreAudio / macOS APIs without cgo |
| [`github.com/jezek/xgb`](https://github.com/jezek/xgb), [`github.com/godbus/dbus`](https://github.com/godbus/dbus) | Global push-to-talk on X11 / Wayland portal |
| [`golang.org/x/sys`](https://pkg.go.dev/golang.org/x/sys) | Platform-native system calls |

---

## 🧪 Testing

```bash
# Run all tests (including the relay server and integration tests)
go test -race ./...

# End-to-end relay handshake, wrong-code rejection, key rotation, kick/ban
go test -run 'TestRelay|TestLAN|TestGroupKey|TestHostKicks' -v ./internal/p2p

# Every UI message has a Turkish translation and fixed-width labels still fit
go test -run 'TestEveryUIMessage|TestFixedColumn' .

# DSP ports (AEC, RNNoise), codec and jitter buffer
go test ./internal/...
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

Copyright © 2026 TheBanri. Limoni Voice is licensed under the
[GNU Affero General Public License v3.0](LICENSE).

You may use, study, change and share it. If you distribute it — changed or not — or let
people use a changed version over a network (a relay server, for example), you must give
them its complete source code under the same license. It cannot be turned into a closed
product.

Versions up to v1.7.0 were released under the MIT License, and stay under it.
The programs and libraries it works with are listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

---

<p align="center">
  <sub>
    <b>Limoni Voice</b> 💛 developed by the open source community.<br/>
    Built with ❤️ using the <a href="https://github.com/thebanri/limoni">Limoni TUI Framework</a>.
  </sub>
</p>
