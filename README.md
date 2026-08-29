<p align="center">
  <img src="assets/logo.png" alt="Limoni Voice Logo" width="220" />
</p>

<h1 align="center">Limoni Voice</h1>
<p align="center">
  <b>Terminal Tabanlı Sıfır-Gecikmeli P2P Şifreli Sesli Konuşma & Ekran Paylaşımı</b><br>
  <i>Built with Limoni TUI & Go</i>
</p>

---

## Özellikler

- **3D Dönen Stüdyo Mikrofonu**: Lobi ekranında Braille Canvas üzerinde 60 FPS akıcılıkta dönen vintage mikrofon.
- **Kolay P2P Anahtarı**: Otomatik üretilen ve tek tuşla ([C]) panoya kopyalanan oda anahtarları.
- **4 Kişilik Sesli Konuşma Odası**: Full-Mesh UDP soketleri üzerinden çalışan 4 kişilik ses odası.
- **Canlı VU-Meter & Ses Görselleştirici**: Her katılımcının ses seviyesi ve RMS düzeyi anlık görselleştirilir.
- **VAD (Voice Activity Detection)**: Konuşan kullanıcının çerçevesi neon yeşil yanarak anlık konuşma durumunu gösterir.
- **Ses Kontrolleri**: [M] Mikrofon Aç/Kapat, [D] Sağırlaştır / Kulaklık Kapat, [+/-] Mikrofon Sesi Seviyesi, [C] Anahtar Kopyala, [Esc] Odadan Ayrıl.

---

## Çalıştırma

Projeyi çalıştırmak için:

```bash
go run .
```

Veya derlenmiş binary'yi çalıştırmak için:

```bash
./limoni-voice
```

### Çoklu Terminalde Test Etme (P2P Mesh Testi)

1. **1. Terminali Açın:**
   - `go run .` çalıştırın.
   - Karşınıza çıkan otomatik Croc anahtarını (örn: `4921-azure-wave`) `[Enter]` ile başlatın.
2. **2. Terminali Açın (Arkadaşınız gibi):**
   - Başka bir sekmede veya pencerede `go run .` çalıştırın.
   - `[Tab]` tuşuna basarak **[3] MEVCUT ODAYA KATIL** alanına geçin.
   - 1. terminaldeki anahtarı yazıp `[Enter]`'a basın.
3. Her iki istemci de anında birbirine bağlanacak ve ses dalgaları karşılıklı senkronize akmaya başlayacaktır!
