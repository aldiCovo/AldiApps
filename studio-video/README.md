# video-animasi

Ketik perintah → Claude menulis naskah & kode animasi → narasi suara dibuat → video dirender di laptop → Anda cek → upload ke YouTube / TikTok / Instagram / Facebook.

```
npm run buat -- "buatkan video tentang cara kerja termos air panas agar panasnya tidak hilang"
```

## Studio Video (aplikasi web lokal)

Klik dua kali **Studio Video** di Desktop (atau `npm run studio`), lalu browser terbuka di http://localhost:4321.
Jendela terminal kecil yang muncul adalah servernya; biarkan terbuka selama memakai Studio.

1. **Gagasan**: tulis ide mentah + atur judul, rasio (9:16 / 16:9), durasi, suara, gaya animasi (motion graphics / kartun 2D), target penonton, gaya narasi.
2. **Master prompt**: AI mengubah gagasan menjadi prompt terstruktur (konfigurasi, tujuan, hook, alur, arahan visual & audio, batasan). Bisa diedit atau disalin.
3. **Storyboard**: AI menjalankan prompt menjadi judul, caption, tag, dan scene (narasi + arahan visual). Semua bisa diedit, scene bisa ditambah/dihapus/diurutkan; ada penghitung kata vs durasi.
4. **Produksi**: suara → kode animasi oleh AI → cek → render di laptop, dengan progres live.
5. **Hasil**: preview video, unduh MP4 / subtitle `.srt` / `storyboard.json`, revisi animasi dengan kalimat biasa, upload ke platform yang sudah disetel.

Riwayat semua video ada di panel kiri. Draf yang belum diproduksi tersimpan otomatis di browser.

## Alur otomatis

1. **Naskah**: Claude membuat judul, caption, hashtag, dan 5–8 scene (narasi + deskripsi visual).
2. **Suara**: setiap narasi diubah jadi suara bahasa Indonesia (Edge TTS, gratis). Durasi scene mengikuti panjang suara.
3. **Kode**: Claude menulis `src/generated/Scenes.tsx` (animasi Remotion, SVG/CSS).
4. **Cek & render**: kode dicek TypeScript lalu dirender jadi MP4 vertikal 1080×1920. Jika error, pesan error dikirim balik ke Claude untuk diperbaiki (maks 4 kali).
5. **Review**: video terbuka otomatis. Pilih `u` untuk upload, `r` untuk revisi (misalnya "teks scene 2 terlalu kecil"), atau `s` untuk selesai.
6. **Upload**: pilih platform. Hasil upload tersimpan di `jobs/<id>/uploads.json`.

Semua hasil per video ada di `jobs/<tanggal-judul>/` (`plan.json`, `Scenes.tsx`, `video.mp4`).
Musik latar (opsional): taruh file `.mp3` bebas royalti di `public/music/`, nanti dipilih acak dengan volume pelan.

## Perintah

| Perintah | Fungsi |
|---|---|
| `npm run studio` | Buka Studio Video (aplikasi web lokal) |
| `npm run buat -- "..."` | Buat video dari perintah |
| `npm run ulang -- <folder-job>` | Buat ulang video dari naskah lama dengan gaya animasi terbaru |
| `npm run upload -- <folder-job>` | Upload ulang video yang sudah ada |
| `npm run dev` | Buka Remotion Studio (preview video terakhir) |
| `npm run auth:youtube` | Login YouTube sekali |
| `npm run auth:tiktok` | Login TikTok sekali |
| `npm run cek` | Cek tipe seluruh kode |

## Setup (sekali saja)

Salin `.env.example` menjadi `.env`, lalu isi bagian berikut.

### 1. Claude (wajib)
Buat API key di https://platform.claude.com, lalu isi `ANTHROPIC_API_KEY`.

### 2. YouTube
1. Buka https://console.cloud.google.com, buat project, lalu aktifkan **YouTube Data API v3**.
2. Buka **OAuth consent screen**, pilih *External*, dan tambahkan email Anda sebagai *Test user*.
3. Buka **Credentials → Create OAuth client ID**, pilih tipe **Desktop app**, lalu salin Client ID & Secret ke `.env`.
4. Jalankan `npm run auth:youtube` dan login.

> Catatan: video dari app Google yang belum diverifikasi (audit) bisa dikunci menjadi *private* oleh YouTube. Ajukan audit API YouTube agar bisa upload publik.

### 3. TikTok
1. Daftar di https://developers.tiktok.com, buat app, lalu tambahkan produk **Login Kit** dan **Content Posting API** (aktifkan *Direct Post*).
2. Isi Redirect URI (harus `https://`, misalnya halaman di domain Anda), lalu salin Client Key/Secret/Redirect URI ke `.env`.
3. Jalankan `npm run auth:tiktok` dan tempel URL hasil redirect.

> Sebelum app lolos audit TikTok, video hanya bisa diposting **private (SELF_ONLY)**. Setelah lolos, ubah `TIKTOK_PRIVACY=PUBLIC_TO_EVERYONE`.

### 4. Instagram & Facebook
Syarat: **Facebook Page** dan akun **Instagram Business/Creator** yang terhubung ke Page tersebut.
1. Buat app di https://developers.facebook.com (tipe *Business*).
2. Di **Graph API Explorer**, buat User Token dengan izin `pages_show_list`, `pages_read_engagement`, `pages_manage_posts`, `instagram_basic`, `instagram_content_publish`, `business_management`.
3. Tukar menjadi token jangka panjang (Access Token Debugger → *Extend*). Lalu panggil `GET /me/accounts` untuk mengambil **Page Access Token** (tidak kedaluwarsa) dan `FB_PAGE_ID`.
4. Panggil `GET /{FB_PAGE_ID}?fields=instagram_business_account` untuk mengambil `IG_USER_ID`.
5. Isi `META_PAGE_TOKEN`, `FB_PAGE_ID`, `IG_USER_ID` di `.env`.

## Gaya visual

Video memakai gaya *kinetic explainer*: shot berganti tiap 1,5–3 detik dengan efek punch-zoom + whoosh, teks besar muncul kata per kata, latar bergantian (gelap / krem / oranye / biru malam) dengan grid, angka berjalan, kartu UI, progress bar, dan subtitle karaoke yang sinkron dengan suara (timing per kata dari Edge TTS).
Komponennya ada di `src/auto/kit.tsx`; ubah warna tema di `THEMES` untuk mengganti identitas visual semua video. Efek suara (`public/sfx/*.wav`) dibuat otomatis.

## Struktur

- `automation/`: pipeline (Claude, TTS, render, upload, login)
  - `pipeline.ts`: mesin produksi yang dipakai bersama CLI & Studio
  - `server.ts` + `web/index.html`: Studio Video (aplikasi web lokal)
  - `config.ts`: pilihan konfigurasi video (rasio, durasi, gaya, suara)
- `src/auto/AutoVideo.tsx`: pembungkus video (urutan scene, transisi, audio narasi, subtitle karaoke, progress bar, musik)
- `src/auto/kit.tsx`: kit motion design (Shots, Kinetic, Counter, Card, Window, Bar, StatCard, ikon, dll.)
- `src/generated/`: kode yang ditulis Claude untuk video terakhir (ditimpa setiap kali membuat video)
- `src/Intro.tsx`: contoh animasi manual

## Render di laptop lain (worker)

Studio bisa jalan di server, sementara render dikerjakan laptop yang lebih kuat. Laptop **menghubungi server**
(bukan sebaliknya), jadi laptop tidak perlu IP publik atau port terbuka.

```
Browser ──> Studio di SERVER (AI, suara, antrean, simpan video, upload)
                    ▲  laptop bertanya tiap 5 detik "ada render?"
                    │  ambil paket kecil (kode + narasi) → render → kirim MP4 per 8 MB
            LAPTOP RENDER (npm run worker)
```

**Di server** (`.env`):
```
RENDER_MODE=remote
WORKER_TOKEN=<token-acak-panjang>     # buat mis. dengan: openssl rand -hex 32
STUDIO_HOST=127.0.0.1                 # atau IP VPN server agar worker bisa terhubung langsung lewat VPN
```

**Di laptop render** (`.env`, project yang sama + `npm install`):
```
STUDIO_URL=http://<ip-vpn-server>:4321   # atau https://studio.aldiab.ai.id
WORKER_TOKEN=<token yang sama>
WORKER_NAME=laptop-i7
```
Lalu jalankan `worker.bat` (atau `npm run worker`).

- Laptop mati: pekerjaan menunggu di antrean, lalu otomatis jalan saat worker online lagi. Status tampil di Studio ("Laptop render: online/offline").
- Worker berhenti di tengah render: setelah 90 detik pekerjaan dikembalikan ke antrean.
- Render error: pesan error dikirim ke server, Claude memperbaiki kode, lalu render diulang (maks 4×).
- Worker merender di folder `.worker/`, jadi aman dipakai bersamaan dengan Studio lokal di laptop yang sama.
- Tombol **Render ulang** di Studio merender lagi dari kode tersimpan tanpa memanggil AI.
