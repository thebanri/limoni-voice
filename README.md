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
  <a href="#-features">English</a> • <a href="#-t%C3%BCrk%C3%A7e-dok%C3%BCmantasyon">Türkçe</a>
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
- **VAD (Voice Activity Detection)**: Real-time speaking detection
- **Noise Suppression**: Multi-stage noise filter (OFF / Standard / High)
- **Live VU-Meter**: Real-time audio level visualization per participant

</td>
<td width="50%">

### 🖥️ Screen Sharing
- **60 FPS Hardware-Accelerated** screen capture
- **Linux**: GPU Screen Recorder + FFmpeg pipeline
- **Windows**: Window & monitor selector with GDI/DXGI capture
- **macOS**: Native system portal integration
- **MPV Player**: Ultra-low-latency viewer experience

</td>
</tr>
<tr>
<td width="50%">

### 🌐 Network Architecture
- **LAN Auto-Discovery**: Zero-configuration local network discovery via broadcast packets
- **Internet P2P**: WebSocket relay server for NAT traversal and hole-punching
- **Relay Server**: Ultra-lightweight Go relay hosted on Railway (~7 MB Docker image)
- **HMAC-SHA256**: Packet integrity and authentication

</td>
<td width="50%">

### 🎨 UI & Aesthetics
- **3D Studio Microphone**: 60 FPS spinning 3D model on Braille Canvas
- **Mouse 3D Rotation**: Interactive control via drag & scroll
- **Animated Modals**: Smooth scaling dialog windows
- **Neon Color Palette**: Modern Cyberpunk-themed TUI design
- **Toast Notifications**: Instant status updates

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
└── assets/              # Logo, icons
```

---

## 🚀 Installation

### Pre-built Binaries (Recommended)

Download the latest release package from the [**Releases**](https://github.com/thebanri/limoni-voice/releases) page:

| Platform | Architecture | File |
|----------|--------------|------|
| 🐧 Linux | amd64 | `limoni-voice_v1.0.0_linux_amd64.tar.gz` |
| 🐧 Linux | arm64 | `limoni-voice_v1.0.0_linux_arm64.tar.gz` |
| 🍎 macOS | Apple Silicon | `Limoni-Voice_v1.0.0_macOS_arm64.app.zip` |
| 🍎 macOS | Intel | `Limoni-Voice_v1.0.0_macOS_amd64.app.zip` |
| 🪟 Windows | amd64 | `Limoni-Voice-Setup_windows_amd64.exe` |
| 🪟 Windows | arm64 | `Limoni-Voice-Setup_windows_arm64.exe` |

```bash
# Linux / macOS
tar xzf limoni-voice_v1.0.0_linux_amd64.tar.gz
./limoni-voice
```

### Building From Source

```bash
# Requirements: Go 1.24+, FFmpeg (optional, for screen sharing)
git clone https://github.com/thebanri/limoni-voice.git
cd limoni-voice
go build -o limoni-voice .
./limoni-voice
```

### Windows Automatic Installer

```powershell
# Install with one command via PowerShell
irm https://raw.githubusercontent.com/thebanri/limoni-voice/main/scripts/install-windows.ps1 | iex
```

### Self-Hosted Relay Server (Docker)

```bash
# Host your own WebSocket relay server
docker build -t limoni-relay .
docker run -p 8080:8080 limoni-relay
```

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
| **Audio Encryption** | AES-256-GCM | Every audio packet is encrypted end-to-end |
| **Packet Authentication** | HMAC-SHA256 | Packet integrity and sender verification |
| **Key Derivation** | SHA-256 | Room key derived uniquely from the passphrase |
| **Magic Header** | `LVS1` | Protocol versioning and header validation |
| **Transport** | WSS (TLS 1.3) | Encrypted WebSocket connection to relay |

> **No audio data is ever stored or inspected by the relay server.** The relay is strictly used for peer discovery and NAT traversal. All audio flows directly peer-to-peer over encrypted UDP.

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

<br/>

---

# 🇹🇷 Türkçe Dokümantasyon

## ✨ Özellikler

### 🎙️ Sesli Konuşma
- **Full-Mesh P2P**: 4 kişilik oda, doğrudan peer-to-peer UDP
- **AES-256-GCM Şifreleme**: Tüm ses ve kontrol paketleri uçtan uca şifreli
- **VAD (Voice Activity Detection)**: Konuşan kişi anlık olarak tespit edilir
- **Gürültü Bastırma**: Çok kademeli filtre sistemi (KAPALI / AÇIK / YÜKSEK)
- **Canlı VU-Meter**: Her katılımcının ses seviyesi gerçek zamanlı görselleştirilir

### 🖥️ Ekran Paylaşımı
- **60 FPS GPU hızlandırmalı** ekran yakalama
- **Linux**: GPU Screen Recorder + FFmpeg pipeline
- **Windows**: Pencere ve ekran bazlı seçim ile GDI/DXGI capture
- **macOS**: Yerel sistem portalı ile entegrasyon
- **MPV Player** ile ultra düşük gecikmeli izleme deneyimi

### 🌐 Ağ Mimarisi
- **LAN Otomatik Keşif**: Broadcast paketleri ile yerel ağda sıfır-konfigürasyon
- **İnternet P2P**: WebSocket relay sunucusu ile NAT geçişi ve hole-punching
- **Relay Sunucusu**: Railway üzerinde barındırılan ultra hafif Go sunucusu (~7 MB Docker image)
- **HMAC-SHA256**: Paket bütünlük doğrulaması

---

## 🎮 Kullanım ve Kısayollar

### Lobi Ekranı

| Tuş | Aksiyon |
|-----|---------|
| `Tab` / `Shift+Tab` | Alanlar arası geçiş |
| `Enter` | Odayı başlat veya katıl |
| `C` / `F2` | Oda anahtarını panoya kopyala |
| `G` / `F3` | Yeni oda anahtarı üret |
| `T` / `F4` | Ses test modalını aç |
| `Esc` | Çıkış onayı |
| `Ctrl+V` | Panodan yapıştır |
| `🖱️ Sürükle` | 3D mikrofonu döndür |
| `🖱️ Scroll` | Yakınlaştır / Uzaklaştır |

### Oda Ekranı

| Tuş | Aksiyon |
|-----|---------|
| `M` | 🎙️ Mikrofon Aç / Kapat |
| `D` | 🔇 Kulaklık Kapat (Sağırlaştır) |
| `N` | 🔊 Gürültü filtresi modunu değiştir |
| `V` | 🖥️ Ekran paylaşımını başlat / durdur |
| `W` | 👁️ Ekran yayınını izle |
| `C` / `F2` | 📋 Oda kodunu kopyala |
| `+` / `-` | 🔉 Mikrofon ses seviyesini ayarla |
| `T` | 🧪 Ses test modalı |
| `Esc` | Odadan ayrıl |

---

## 🤝 Katkıda Bulunma

1. Bu repoyu **fork** edin
2. Feature branch oluşturun (`git checkout -b feature/harika-ozellik`)
3. Değişikliklerinizi commit edin (`git commit -m 'feat: harika özellik eklendi'`)
4. Branch'inizi push edin (`git push origin feature/harika-ozellik`)
5. **Pull Request** açın

---

## 📄 Lisans

Bu proje [MIT Lisansı](LICENSE) altında lisanslanmıştır.

---

<p align="center">
  <sub>
    <b>Limoni Voice</b> 💛 developed by the open source community.<br/>
    Built with ❤️ using the <a href="https://github.com/thebanri/limoni">Limoni TUI Framework</a>.
  </sub>
</p>
