<p align="center">
  <img src="assets/logo.png" alt="Limoni Voice Logo" width="200" />
</p>

<h1 align="center">🍋 Limoni Voice</h1>

<p align="center">
  <b>Terminal Tabanlı • Uçtan Uca Şifreli • P2P Sesli Konuşma & Ekran Paylaşımı</b>
</p>

<p align="center">
  <a href="#-kurulum"><img src="https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-blue?style=for-the-badge" alt="Platform"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-green?style=for-the-badge" alt="License"></a>
  <a href="#"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go"></a>
  <a href="#"><img src="https://img.shields.io/badge/Encryption-AES--256--GCM-critical?style=for-the-badge&logo=letsencrypt&logoColor=white" alt="Encryption"></a>
</p>

<p align="center">
  <i>Tamamen terminal içinde çalışan, sıfır bağımlılıklı, gerçek zamanlı P2P sesli konuşma uygulaması.<br/>
  <a href="https://github.com/thebanri/limoni">Limoni TUI Framework</a> ile Go dilinde yazılmıştır.</i>
</p>

<p align="center">
  <a href="README.md">English</a> • <b>Türkçe</b>
</p>

<p align="center">
  <img src="assets/screenshot.gif" alt="Limoni Voice Önizleme" width="85%" />
</p>

---

## ✨ Özellikler

<table>
<tr>
<td width="50%">

### 🎙️ Sesli Konuşma
- **Full-Mesh P2P**: 4 kişilik oda, doğrudan peer-to-peer UDP
- **AES-256-GCM Şifreleme**: Tüm ses ve kontrol paketleri uçtan uca şifreli
- **VAD (Voice Activity Detection)**: 60ms dairesel pre-roll tamponu ile konuşan anlık tespit edilir
- **Harf & Başlangıç Koruma**: Tepe-RMS (Crest Factor) ayrıştırması ile kelime başlangıçlarındaki ötümsüz seslerin (örn. "selam"daki "s" harfi) kesilmesi önlenir
- **Gürültü Bastırma**: Mekanik klavye ve darbe filtreli çok kademeli filtre (KAPALI / AÇIK / YÜKSEK)
- **Canlı VU-Meter**: Her katılımcının ses seviyesi gerçek zamanlı görselleştirilir

</td>
<td width="50%">

### 🖥️ Ekran Paylaşımı
- **60 FPS Donanım Hızlandırmalı** ekran yakalama (1080p, ultra düşük gecikme)
- **Yerel Platform API Desteği**:
  - **🪟 Windows**: ✅ **Test Edildi & Sorunsuz Çalışıyor** (Win32 GDI & DWM pencere yakalama / FFmpeg gdigrab ekran yakalama ile pencere ve monitör seçimi)
  - **🍎 macOS**: ✅ **Test Edildi & Sorunsuz Çalışıyor** (Yerel ScreenCaptureKit & CoreMedia API'leri ile donanım hızlandırmalı yakalama)
  - **🐧 Linux (GNOME)**: ✅ **Test Edildi & Sorunsuz Çalışıyor** (Doğrudan Mutter PipeWire tam ekran ve Portal pencere seçici)
  - **🐧 Linux (KDE Plasma)**: ✅ **Test Edildi & Sorunsuz Çalışıyor** (XDG Desktop Portal PipeWire ekran & pencere seçimi)
  - **🐧 Diğer Linux Ortamları (Hyprland, Sway, XFCE vb.)**: ⚠️ *Deneysel / Henüz Test Edilmedi* (GPU Screen Recorder / FFmpeg fallback)
- **MPV / FFplay** ile ultra düşük gecikmeli izleme deneyimi

</td>
</tr>
<tr>
<td width="50%">

### 🌐 Ağ Mimarisi
- **LAN Otomatik Keşif**: Broadcast paketleri ile yerel ağda sıfır-konfigürasyon
- **İnternet P2P**: WebSocket relay sunucusu ile NAT geçişi ve hole-punching
- **Dinamik Port Hopping**: DPI ve sansür engellemelerine karşı otomatik port rotasyonu
- **Anti-Replay Koruması**: Zaman damgası penceresi ve kayan sıra önbelleği
- **Relay Sunucusu**: Railway üzerinde barındırılan ultra hafif Go sunucusu (~7 MB Docker image)

</td>
<td width="50%">

### 🎨 Arayüz & Deneyim
- **3D Stüdyo Mikrofonu**: Braille Canvas üzerinde 60 FPS dönen 3D model
- **Fare ile 3D Döndürme**: Drag & scroll ile interaktif kontrol
- **Animasyonlu Modallar**: Yumuşak geçişli dialog pencereleri
- **Neon & Cyberpunk Paletleri**: Çoklu temalar (Neon, Cyberpunk, Synthwave, Monokai, Dracula)
- **Canlı VU-Meter**: Gerçek zamanlı ses dalga formu ve seviye görselleştirme
- **Toast Bildirimleri**: Anlık durum mesajları

</td>
</tr>
<tr>
<td width="50%">

### 📁 Uçtan Uca Dosya & Kod Paylaşımı
- **Doğrudan P2P Transfer**: Parçalı ve uçtan uca şifreli dosya & kod paylaşımı
- **Güvenlik Karantinası**: Çalıştırılabilir dosya uyarıları ve sıkı dosya adı sanitizasyonu
- **Otomatik Kayıt**: Kabul edilen dosyalar doğrudan `Downloads/LimoniTransfers` klasörüne kaydedilir
- **Bütünlük Denetimi**: Otomatik SHA-256 sağlama doğrulaması

</td>
<td width="50%">

### 💬 Sohbet & Oda Güvenliği
- **Terminal İçi Chat**: Çok satırlı metin yazımı, tıklanabilir linkler & slash komutları (`/help`, `/clear`)
- **Oda Kilidi & PIN**: 4 haneli PIN koruması (`/lock <pin>`) ve host kilit yönetimi
- **Bas-Konuş (PTT)**: Ayarlanabilir bas-konuş tuşu ve konuşma algılama
- **Kişi Bazlı Ses Ayarı**: Katılımcı başına bağımsız ses seviyesi ve AGC güçlendirme

</td>
</tr>
</table>

---

## 🏗️ Mimari

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

### Proje Yapısı

```
limoni-voice/
├── main.go              # Uygulama giriş noktası, event loop & ekran yönetimi
├── network.go           # P2P mesh ağı, şifreleme, WebSocket relay istemcisi
├── audio.go             # Ses motoru: yakalama, oynatma, VAD, gürültü filtresi
├── ui_lobby.go          # Lobi ekranı: 3D mikrofon, input alanları, menü
├── ui_room.go           # Oda ekranı: katılımcı kartları, VU-meter, loglar
├── dialogs.go           # Modal dialoglar: çıkış, ayrılma, ses testi, ekran paylaşımı
├── microphone3d.go      # 3D stüdyo mikrofon modeli (polygon & wireframe)
├── screenshare/         # Ekran paylaşımı modülü (GPU Rec, FFmpeg, MPV)
├── clipboard.go         # Platformlar arası pano desteği
├── roomcode.go          # Croc tarzı oda kodu üreteci
├── relay-server/        # WebSocket relay sunucusu (bağımsız Go modülü)
├── scripts/             # Build & paketleme scriptleri
├── release_assets/      # Derlenmiş binary'ler ve installer'lar
├── dist/                # Platform bazlı dağıtım paketleri
└── assets/              # Logo, ikonlar
```

---

## 🚀 Kurulum

### 🐧 Linux & 🍎 macOS Tek Komutla Kurulum (Önerilen)

Terminalinizde tek bir komut çalıştırarak Limoni Voice'u doğrudan kurabilirsiniz:

```bash
curl -fsSL https://raw.githubusercontent.com/thebanri/limoni-voice/main/install.sh | bash
```

### 🪟 Windows Tek Komutla Kurulum (PowerShell)

```powershell
irm https://raw.githubusercontent.com/thebanri/limoni-voice/main/scripts/install-windows.ps1 | iex
```

### Ön Gereksinimler (Ses ve Ekran Paylaşımı)

> [!IMPORTANT]
> - **🪟 Windows**: Sesli konuşma tamamen **sıfır bağımlılıkla** doğrudan çalışır (yerel Win32 `winmm` ses API'leri kullanılır). Ekran paylaşımı için **FFmpeg** ve **MPV** gereklidir.
> - **🐧 Linux**: Standart PulseAudio, PipeWire veya ALSA bulunan Linux dağıtımlarında sesli konuşma doğrudan çalışır. Ekran paylaşımı için **FFmpeg** ve **MPV** gereklidir.
> - **🍎 macOS**: macOS işletim sisteminde yerleşik komut satırı ses yakalama aracı bulunmadığından, hem sesli konuşma hem de ekran paylaşımı için **FFmpeg** ve **MPV** (Homebrew ile) kurulmalıdır:
>   ```bash
>   brew install ffmpeg mpv
>   ```
> - **🪟 Windows (winget / choco / scoop)**:
>   ```powershell
>   # winget ile
>   winget install -e --id Gyan.FFmpeg
>   winget install -e --id shinchiro.mpv
>
>   # veya Chocolatey ile
>   choco install ffmpeg mpv
>
>   # veya Scoop ile
>   scoop install ffmpeg mpv
>   ```
>   *(Not: `Limoni-Voice-Setup.exe` kurulum sihirbazı bunları sizin için otomatik olarak da kurabilir).*
> - **🐧 Linux (Debian / Ubuntu / Arch / Fedora)**:
>   ```bash
>   # Debian / Ubuntu
>   sudo apt install ffmpeg mpv
>   # Arch Linux
>   sudo pacman -S ffmpeg mpv
>   # Fedora
>   sudo dnf install ffmpeg mpv
>   ```

### Hazır Paketler (Manuel İndirme)

[**Releases**](https://github.com/thebanri/limoni-voice/releases) sayfasından platformunuza uygun paketi indirin:

| Platform | Mimari | Dosya |
|----------|--------|-------|
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

### Kaynaktan Derleme

```bash
# Gereksinimler: Go 1.24+
git clone https://github.com/thebanri/limoni-voice.git
cd limoni-voice
go build -o limoni-voice .
./limoni-voice
```

### Docker ile Kendi Relay Sunucunuzu Barındırma

Sunucu varsayılan olarak **27850** portunu kullanır (8080 gibi yaygın portlarla çakışmayı ve internet taramalarını engellemek için). İsteğe bağlı olarak `.env` dosyasından `PORT` değerini istediğiniz herhangi bir porta değiştirebilirsiniz.

Ayrıca yabancıların izinsiz kullanmasını engellemek için **Erişim Parolası / Token (`RELAY_AUTH_TOKEN`)** tanımlayabilirsiniz:

```bash
# Yöntem 1: Docker Compose ile (Önerilen)
cp .env.example .env
# .env dosyasında PORT ve RELAY_AUTH_TOKEN belirleyin
docker compose up -d

# Yöntem 2: Standart Docker Komutu ile
docker build -t limoni-relay .
# Parola korumalı güvenli mod (Varsayılan Port 27850):
docker run -d --name limoni-relay -p 27850:27850 -e RELAY_AUTH_TOKEN="gizli_anahtar_123" limoni-relay
```

#### İstemcileri Kendi Korumalı Sunucunuza Bağlama:

##### 1. Uygulama İçi Grafik Arayüzden (Önerilen - Arkadaşlarınız İçin Kolay):
Arkadaşlarınızın komut satırıyla uğraşmaması için doğrudan uygulama içinden ayarlayabilirsiniz:
- Lobideyken **`[R]`** tuşuna basın veya alt bardaki **`[ R : Relay & Security Settings ]`** butonuna tıklayın.
- Herhangi bir ekranda **`F5`** kısayoluna basın.
- Oda içindeyken sohbete **`/relay`** veya **`/server`** yazın.

Açılan animasyonlu pencerede **WebSocket Adresini** (örn. `wss://funny-animal-1234.trycloudflare.com/ws` veya `ws://192.168.1.100:27850/ws`) ve sunucu şifresini yazıp **`[ Kaydet ve Bağlan ]`** deyin. Ayarlar kalıcı olarak kaydedilir (`settings.json`).

##### 2. Komut Satırı veya Ortam Değişkenleri ile:
```bash
# Komut satırı parametresi ile:
./limoni-voice --relay ws://192.168.1.100:27850/ws --relay-token gizli_anahtar_123

# VEYA doğrudan URL içinde:
./limoni-voice --relay "ws://192.168.1.100:27850/ws?token=gizli_anahtar_123"

# Alternatif olarak ortam değişkenleriyle:
export LIMONI_RELAY_URL="ws://192.168.1.100:27850/ws"
export LIMONI_RELAY_TOKEN="gizli_anahtar_123"
./limoni-voice
```

#### 🌐 Cloudflare Tunnel ile Dış Dünyaya Açma (Modemden Port Açmadan)

Eğer sunucuyu kendi ev bilgisayarınızda çalıştırıyorsanız, modeminizden port açmanıza gerek kalmadan **Cloudflare Tunnel** ile güvenli, DDoS korumalı ve otomatik SSL (WSS) sertifikalı bir dış bağlantı oluşturabilirsiniz.

##### 🚀 Seçenek 1: Hızlı Tünel (Token veya Cloudflare Hesabı GEREKMEZ)
Hiçbir Cloudflare hesabı açmadan, token girmeden anında ücretsiz bir dış bağlantı URL'i almak için:
```bash
# 1. Relay sunucusunu ve hızlı tüneli tek komutla başlatın:
docker compose --profile quick-tunnel up -d

# 2. Cloudflare'in size atadığı ücretsiz trycloudflare adresini görün:
docker logs limoni-quick-tunnel
# Çıktıda şunu göreceksiniz:
# https://funny-animal-1234.trycloudflare.com

# 3. Siz ve arkadaşınız bu adrese WSS ile bağlanın:
./limoni-voice --relay wss://funny-animal-1234.trycloudflare.com/ws --relay-token gizli_anahtar_123
```

##### 🔑 Seçenek 2: Kendi Alan Adınız ile (Kalıcı Tünel Token'ı)
Cloudflare Zero Trust panelinden oluşturduğunuz tünelin token'ını `.env` dosyasına `CLOUDFLARE_TUNNEL_TOKEN=eyJh...` şeklinde ekleyin ve çalıştırın:
```bash
docker compose --profile tunnel up -d
```

##### 💻 Seçenek 3: Doğrudan `cloudflared` CLI ile
```bash
# cloudflared yüklü ise terminalden tek satırla:
cloudflared tunnel --url http://localhost:27850
```

##### 🛑 Sunucu ve Tünelleri Durdurma
Tünel servisleri Docker Compose profilleri altında tanımlı olduğundan, düz bir `docker compose down` komutu yalnızca ana relay servisini kapatır. Tüneller dahil tüm servisleri tek seferde durdurmak için:

```bash
# Tüm profillerdeki tünelleri ve relay'i birlikte kapatıp kaldırır:
docker compose --profile "*" down

# Veya sadece kullandığınız profili kapatmak için:
docker compose --profile quick-tunnel down

# Arkada kalan yetim/eski konteynerleri temizlemek için:
docker compose down --remove-orphans
```

> [!TIP]
> **Ağ / UDP QUIC Bağlantı Sorunları:** `docker-compose.yml`, tünel trafiğini UDP port 7844 (QUIC) yerine standart HTTPS (TCP 443) üzerinden geçirmek üzere `--protocol http2` ve genel DNS ile yapılandırılmıştır. Bu sayede servis sağlayıcıların UDP engellemelerine ve "sendmsg: network is unreachable" hatalarına takılmaz.

---

### LAN Modu (Çevrimdışı / İnternetsiz Yerel Ağ)

Limoni Voice'u internet erişimi olmayan izole yerel ağlarda çalıştırmak için:

```bash
# Doğrudan LAN P2P modunda başlat (Relay sunucusu devre dışı bırakılır)
./limoni-voice --lan

# Veya ortam değişkeniyle:
export LIMONI_LAN_ONLY=1
./limoni-voice
```

---

## ⚙️ Komut Satırı Parametreleri & Konfigürasyon

| Parametre | Ortam Değişkeni | Varsayılan | Açıklama |
|-----------|-----------------|------------|----------|
| `--relay <url>` | `LIMONI_RELAY_URL` | `wss://limoni-voice-production.up.railway.app/ws` | Kendi relay sunucunuzun WebSocket adresi |
| `--relay-token <token>` | `LIMONI_RELAY_TOKEN` | `""` | Parola korumalı relay sunucuları için kimlik doğrulama anahtarı |
| `--token <token>` | `LIMONI_RELAY_TOKEN` | `""` | `--relay-token` parametresinin takma adı |
| `--lan`, `--lan-only` | `LIMONI_LAN_ONLY` | `false` | Sadece yerel ağ modunu zorlar (internet relay'i kapatır) |
| `--offline` | `LIMONI_OFFLINE` | `false` | `--lan` parametresinin takma adı |
| `--peer <ip:port>` | `LIMONI_PEER` | `""` | Farklı alt ağlar veya VPN için doğrudan hedef eş IP/adresi |
| `--connect <ip:port>` | `LIMONI_PEER` | `""` | `--peer` parametresinin takma adı |
| `--version` | - | - | Sürüm bilgisini gösterir |
| `--help`, `-h` | - | - | Yardım ve kullanım parametrelerini listeler |

---

## 🎮 Kullanım ve Kısayollar

### Hızlı Başlangıç

```bash
# 1. Uygulamayı başlatın
./limoni-voice

# 2. Otomatik oluşturulan oda anahtarınız karşınıza çıkar (örn: 9421-azure-wave)
# 3. [Enter] ile odayı başlatın
# 4. Arkadaşınıza anahtarı gönderin!
```

### Lobi Ekranı Kısayolları

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

### Oda Ekranı Kısayolları

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

## 🔐 Güvenlik

Limoni Voice, güvenliği temel bir prensip olarak ele alır:

| Katman | Teknoloji | Açıklama |
|--------|-----------|----------|
| **Uçtan Uca Şifreleme** | AES-256-GCM | Tüm ses, sohbet, kontrol ve dosya paketleri uçtan uca şifrelenir |
| **Paket Doğrulama** | AES-256-GCM AEAD Tag | 128-bit GHASH doğrulama etiketi ile paket bütünlüğü garanti edilir |
| **Anahtar Türetme** | Salted HMAC-SHA256 | Oda kodundan benzersiz tuz (salt) ile türetilen kriptografik anahtar |
| **Replay Attack Koruması** | Zaman Damgası + Önbellek | 30s tazelik kontrolü ve kayan pencere ile paket tekrarı engellenir |
| **Girdi Temizleme** | Sıkı Dosya Filtresi | Yol atlama (path traversal), kabuk komutları ve tehlikeli formatlar karantinaya alınır |
| **Magic Prefix** | `LVS1` | Protokol versiyonu ve paket doğrulama başlığı |
| **Transport** | WSS (TLS 1.3) / UDP | Sinyalleşme için şifreli WebSocket, medya için doğrudan şifreli UDP |

> **Hiçbir ses veya dosya verisi relay sunucusunda işlenmez veya depolanmaz.** Relay yalnızca peer keşfi ve NAT traversal için kullanılır. Gerçek iletişim doğrudan peer-to-peer UDP üzerinden gerçekleşir.

---

## 🛠️ Bağımlılıklar

### Çalışma Zamanı (Opsiyonel)

| Araç | Platform | Amaç |
|------|----------|------|
| [FFmpeg](https://ffmpeg.org) | Tümü | Video encoding/transcoding |
| [GPU Screen Recorder](https://git.dec05eba.com/gpu-screen-recorder) | Linux | GPU hızlandırmalı ekran yakalama |
| [MPV](https://mpv.io) | Tümü | Düşük gecikmeli video oynatıcı |

### Go Modülleri

| Modül | Amaç |
|-------|------|
| [`github.com/thebanri/limoni`](https://github.com/thebanri/limoni) | TUI framework (terminal, widget, grafik, animasyon) |
| [`github.com/gorilla/websocket`](https://github.com/gorilla/websocket) | WebSocket relay istemcisi |
| [`golang.org/x/sys`](https://pkg.go.dev/golang.org/x/sys) | Platform-native sistem çağrıları |

---

## 🧪 Testler

```bash
# Tüm testleri çalıştır
go test -v ./...

# Sadece birim testleri
go test -v -run TestRoomCode
go test -v -run TestAudioEngine

# Relay sunucu testleri
cd relay-server && go test -v ./...
```

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
    <b>Limoni Voice</b> 💛 açık kaynak topluluğu tarafından geliştirilmektedir.<br/>
    <a href="https://github.com/thebanri/limoni">Limoni TUI Framework</a> ile ❤️ ile yapılmıştır.
  </sub>
</p>
