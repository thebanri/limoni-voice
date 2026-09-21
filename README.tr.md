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
  <i>Tamamen terminal içinde çalışan, saf Go ile yazılmış (cgo yok), tek dosyalık, gerçek zamanlı P2P sesli konuşma uygulaması.<br/>
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
- **Opus 48 kHz**: 32 kbps CBR, bant içi FEC ve paket kaybına göre ayarlanan kodlama
- **Uyarlanabilir Jitter Tamponu**: RFC 3550 jitter tahmini, kayıp gizleme (PLC) ve FEC kurtarma
- **Yankı Giderme (AEC)**: Saf Go Speex MDF portu — kulaklıksız hoparlörle konuşabilirsiniz
- **Gürültü & Tıkırtı Bastırma**: Çok bantlı filtre veya saf Go RNNoise (KAPALI / AÇIK / YÜKSEK / AI) ve klavye tıkırtısı / alkış için ileriye bakan ani ses bastırıcı
- **Ses Yumuşatma**: Sertliği alan EQ, yumuşak kompresör ve sınırlayıcı ile dengeli, yormayan ses seviyesi
- **VAD**: Pre-roll tamponu ve kelime başı korumasıyla anlık konuşma algılama
- **Global Bas-Konuş**: Terminal odakta değilken de çalışır (X11, Wayland'de XDG portal, Win32, macOS)
- **Yerel Ses G/Ç**: PulseAudio/PipeWire protokolü, CoreAudio, winmm — harici araç gerekmez

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
- **Sadece İzleyene Gönderim**: Görüntü yalnızca gerçekten izleyen kişilere yüklenir (izleyen yoksa bant genişliği harcanmaz)
- **Kalite Ön Ayarları & Uyarlanabilir Bit Hızı**: 720p30'dan (varsayılan, 2,5 Mbps) 1080p120'ye; izleyiciler paket kaybederse yayın kendiliğinden düşer
- **Kayıp Telafisi**: NACK ile yeniden gönderim, zamana bağlı sıralama tamponu ve yayılmış gönderim; yeni izleyici son anahtar kareden anında başlar
- **Sistem Sesi**: Bilgisayarda çalan sesi paylaşın (PipeWire/PulseAudio monitör, WASAPI loopback, ScreenCaptureKit); kendi sesli sohbetiniz otomatik çıkarılır
- **MPV / FFplay** ile ultra düşük gecikmeli izleme (akış özel bir boru ile verilir)

</td>
</tr>
<tr>
<td width="50%">

### 🌐 Ağ Mimarisi
- **LAN Otomatik Keşif**: Broadcast paketleri ile yerel ağda sıfır-konfigürasyon
- **Yol Merdiveni**: LAN → doğrudan P2P (IPv6 / IPv4 hole-punch) → UDP relay → WebSocket relay
- **NAT Sınıflandırma**: STUN ile cone/symmetric tespiti, port püskürtme ve çoklu soket delme
- **UDP Relay**: Doğrudan yol kurulamazsa (symmetric NAT, CGNAT) düşük gecikmeli şifreli medya aktarımı
- **Yedekli Ses**: Ses hem relay hem doğrudan yoldan gönderilir, alıcı tekrarları ayıklar
- **Canlı Tanılama**: Debug panelinde (`F12`) ve `/net` komutunda peer başına yol, RTT, kayıp ve jitter
- **Relay Sunucusu**: Prometheus `/metrics` ve yapılandırılmış log destekli hafif Go sunucusu

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

### Proje Yapısı

```
limoni-voice/
├── main.go              # Başlatıcı: bayraklar, terminal, düğüm
├── app*.go              # Uygulama durumu, tuş/fare yönlendirme, render, ayarlar
├── network*.go          # P2P düğüm: relay, el sıkışma, NAT, medya, dosya, istatistik
├── audio.go             # Ses motoru: 48 kHz yakalama/oynatma, VAD, AEC, gürültü
├── ui_lobby.go          # Lobi ekranı: 3D mikrofon, girişler, menü
├── ui_room.go           # Oda ekranı: katılımcı kartları, VU-meter, sohbet
├── dialogs.go           # Modallar: ses ayarları, relay, debug/tanılama, paylaşım
├── internal/
│   ├── protocol/        # İkili paket formatı + relay sinyal tipleri
│   ├── e2ee/            # Oda kodu, CPace PAKE el sıkışma, epoch anahtarlığı
│   ├── relay/           # Relay sunucusu (WebSocket + UDP) ve metrikler
│   ├── nat/             # STUN NAT sınıflandırma, delme stratejileri, IPv6
│   ├── voice/           # Opus codec, uyarlanabilir jitter tamponu
│   ├── dsp/             # Speex MDF yankı giderici, RNNoise, FFT
│   ├── audioio/         # PulseAudio / CoreAudio / winmm / tool fallback
│   └── ptt/             # Sistem geneli bas-konuş (X11, portal, Win32, macOS)
├── screenshare/         # Ekran paylaşımı modülü (GPU Rec, FFmpeg, MPV)
├── relay-server/        # Relay binary'si (internal/relay üzerinde ince katman)
├── cmd/limoni-sign/     # Sürüm checksum'ları için ed25519 imzalama aracı
└── scripts/             # Build & paketleme scriptleri
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
> - **🍎 macOS**: Sesli konuşma yerel CoreAudio kullanır (bağımlılık gerekmez). Ekran paylaşımı için Homebrew ile **FFmpeg** ve **MPV** kurun:
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
# Gereksinimler: Go 1.26+
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

#### ⚡ Düşük Gecikme: UDP Relay (Cloudflare Tunnel kullananlar için önemli)

Cloudflare Tunnel yalnızca **TCP/WebSocket** taşır. İki kişi doğrudan bağlanamadığında (ör. symmetric NAT veya CGNAT) ses tünel içindeki WebSocket relay'e düşer ve bu genellikle **~100+ ms** ekler. En düşük gecikme için:

1. Relay'i bir VPS'te çalıştırın veya modeminizde **UDP 27850** portunu yönlendirin.
2. İstemcilere UDP relay adresini bildirin:
   ```bash
   RELAY_UDP_PUBLIC_ADDR=203.0.113.10:27850 docker compose --profile tunnel up -d
   ```
3. Debug panelini (`F12`) açın veya sohbete `/net` yazın: peer yolu `Relay` yerine `P2P`, `P2P-v6` ya da `Relay-UDP` görünmelidir.

| Ortam Değişkeni | Varsayılan | Açıklama |
|-----------------|------------|----------|
| `PORT` | `27850` | HTTP/WebSocket TCP portu |
| `UDP_PORT` | `PORT` ile aynı | UDP relay portu (`0` / `off` kapatır) |
| `RELAY_UDP_PUBLIC_ADDR` | – | UDP için duyurulan `host:port` (tünel arkasında zorunlu) |
| `RELAY_AUTH_TOKEN` | – | İstemciler için isteğe bağlı parola |
| `TRUST_PROXY` | `false` | `CF-Connecting-IP` / `X-Forwarded-For` başlıklarına her zaman güven (özel ağdaki proxy'lere zaten güvenilir) |
| `LOG_FORMAT` / `LOG_LEVEL` | `json` / `info` | Log biçimi ve seviyesi |

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
| `--relay <url>` | `LIMONI_RELAY_URL` | `wss://relay.thebanri.dpdns.org/ws` | Kendi relay sunucunuzun WebSocket adresi |
| `--relay-token <token>` | `LIMONI_RELAY_TOKEN` | `""` | Parola korumalı relay sunucuları için kimlik doğrulama anahtarı |
| `--token <token>` | `LIMONI_RELAY_TOKEN` | `""` | `--relay-token` parametresinin takma adı |
| `--lan`, `--lan-only` | `LIMONI_LAN_ONLY` | `false` | Sadece yerel ağ modunu zorlar (internet relay'i kapatır) |
| `--offline` | `LIMONI_OFFLINE` | `false` | `--lan` parametresinin takma adı |
| `--peer <ip:port>` | `LIMONI_PEER` | `""` | Farklı alt ağlar veya VPN için doğrudan hedef eş IP/adresi |
| `--connect <ip:port>` | `LIMONI_PEER` | `""` | `--peer` parametresinin takma adı |
| `--sysaudio-test` | - | - | Sistem sesini 5 sn yakalar, seviyeyi yazdırır ve çıkar (ekran paylaşımı sesi teşhisi) |
| `--version` | - | - | Sürüm bilgisini gösterir |
| `--help`, `-h` | - | - | Yardım ve kullanım parametrelerini listeler |

---

### 🖥️ Ekran Paylaşımı İpuçları

- Odada **`V`** tuşuna basın, **kalite ön ayarını** seçin (`1`–`5` veya `Q` ile sırayla) ve **sistem sesini** **`A`** ile açıp kapatın. Eksik araçlar ve kurulum komutu pencerede gösterilir.
- Biri izlemeye başlayana kadar hiçbir şey yüklenmez (`W` veya yayına tıklama). Sonra bit hızı izleyicinin bağlantısına uyar (değişirken kısa bir takılma olur).
- Debug panelinde (**`F12`**) ön ayar, anlık bit hızı, izleyici sayısı, gönderim kuyruğu ve yeniden gönderimler görünür.
- **Relay sunucusu işletenler:** relay üzerinden kişiye özel gönderim için relay sunucusunu güncelleyin; eski relay'ler de çalışır ama relay'e düşen görüntüyü tüm odaya iletir.
- **Sistem sesi paylaşılmıyorsa:** `limoni-voice --sysaudio-test` komutunu çalıştırın; yakalama arka ucunu ve siz bir şey çalarken seviye çubuğunu gösterir. Windows'ta varsayılan çıkış aygıtı yakalanır (Ses ayarları → Çıkış); o aygıtta ses çalmıyorsa hiçbir veri gelmez.
- **macOS:** sürüm paketleri yakalama yardımcısını içerir; kendiniz derlerseniz ilk kullanımda derlenir (`xcode-select --install`). Sistem sesi macOS 13+ ister.

## 🎮 Kullanım ve Kısayollar

### Hızlı Başlangıç

```bash
# 1. Uygulamayı başlatın
./limoni-voice

# 2. Otomatik oluşturulan oda anahtarınız karşınıza çıkar (örn: 9421-azure-wave-lemon)
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
| `P` | 🎚️ Ses algılama / Bas-konuş modunu değiştir |
| `E` | 🔁 Yankı gidermeyi aç / kapat |
| `S` | 🪶 Ses yumuşatmayı aç / kapat (yumuşak tizler, dengeli ses, tepe sınırlayıcı) |
| `F12` | 🩺 Debug & ağ tanılama paneli |
| `/net`, `/stats` | 📶 Peer başına yol, RTT, kayıp ve jitter bilgisini sohbete yaz |
| `Esc` | Odadan ayrıl |

---

## 🔐 Güvenlik

| Katman | Teknoloji | Açıklama |
|--------|-----------|----------|
| **Oda Kodu** | `NNNN-kelime-kelime-kelime` | 4 hane relay'in gördüğü genel oda kimliğidir; 3 kelime gizli anahtardır ve cihazınızdan çıkmaz |
| **Kimlik Doğrulama** | CPace PAKE (ristretto255) | Katılan kişi kodu açıklamadan bildiğini kanıtlar; yakalanan trafikten çevrimdışı tahmin yapılamaz |
| **Grup Anahtarı** | Rastgele AES-256 epoch anahtarı | Host üretir, PAKE kanalıyla iletir; üye katıldığında veya ayrıldığında, host değiştiğinde ya da port atlandığında yenilenir |
| **Anahtar Yenileme** | X25519 üye anahtarları | Her üyenin oturum anahtarını host PAKE kanalı içinde onaylar. Yeni grup anahtarı her üyeye ayrı ayrı mühürlenir; ayrılan üye onu okuyamaz, hiçbir üye sahtesini üretemez; yeni seçilen host da aynı yolla anahtar yeniler |
| **Şifreleme** | AES-256-GCM | Ses, sohbet, kontrol, video ve dosya paketleri rastgele nonce ile uçtan uca şifrelenir |
| **Replay Koruması** | Sıra + zaman penceresi | Tazelik penceresi ve kayan tekrar önbelleği |
| **Host Onayı** | Oda kilidi & PIN | Host odayı kilitleyip host tarafında doğrulanan 4 haneli PIN isteyebilir |
| **Güncellemeler** | SHA-256 + ed25519 | Otomatik güncelleme `checksums.txt` eşleşmeyen dosyayı reddeder; açık anahtar gömülüyse imza da doğrulanır |
| **Transport** | WSS (TLS) / UDP | Sinyalleşme WebSocket üzerinden, medya doğrudan veya relay üzerinden şifreli UDP ile |

> **Relay hiçbir şeyi çözemez.** Yalnızca sayısal oda kimliğini, anlamsız PAKE mesajlarını ve şifreli medya çerçevelerini görür. Doğrudan yol kurulamadığında şifreli ses relay üzerinden (mümkünse UDP, değilse WebSocket) iletilir.

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
| [`github.com/gorilla/websocket`](https://github.com/gorilla/websocket) | WebSocket relay taşıması |
| [`github.com/thesyncim/gopus`](https://github.com/thesyncim/gopus) | Saf Go Opus codec |
| [`filippo.io/cpace`](https://pkg.go.dev/filippo.io/cpace) | CPace parola doğrulamalı anahtar değişimi |
| [`github.com/jfreymuth/pulse`](https://github.com/jfreymuth/pulse) | Yerel PulseAudio/PipeWire istemcisi |
| [`github.com/ebitengine/purego`](https://github.com/ebitengine/purego) | cgo'suz CoreAudio / macOS API'leri |
| [`github.com/jezek/xgb`](https://github.com/jezek/xgb), [`github.com/godbus/dbus`](https://github.com/godbus/dbus) | X11 / Wayland portal üzerinde global bas-konuş |
| [`golang.org/x/sys`](https://pkg.go.dev/golang.org/x/sys) | Platform-native sistem çağrıları |

---

## 🧪 Testler

```bash
# Tüm testler (relay sunucusu ve entegrasyon testleri dahil)
go test -race ./...

# Uçtan uca relay el sıkışma, yanlış kod reddi ve anahtar yenileme
go test -run 'TestRelay|TestLAN|TestGroupKey' -v .

# DSP portları (AEC, RNNoise), codec ve jitter tamponu
go test ./internal/...
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
