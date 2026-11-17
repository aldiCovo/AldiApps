// Konfigurasi satu video (diisi dari form Studio atau default CLI).

export type Rasio = "9:16" | "16:9";
export type Gaya = "kinetic" | "kartun";

export type VideoConfig = {
  topik: string; // gagasan/ide mentah dari pengguna
  judul?: string; // opsional; kosong = dibuat AI
  durasi: number; // target detik
  rasio: Rasio;
  gaya: Gaya;
  audiens: string;
  gayaNarasi: string;
  suara: string; // id suara Edge TTS
  catatan?: string; // permintaan tambahan bebas
};

export const DEFAULT_CONFIG: VideoConfig = {
  topik: "",
  durasi: 45,
  rasio: "9:16",
  gaya: "kinetic",
  audiens: "Umum, remaja dan dewasa muda Indonesia",
  gayaNarasi: "Santai, cepat, bertenaga, tetap sopan",
  suara: "id-ID-GadisNeural",
};

export const GAYA_LABEL: Record<Gaya, string> = {
  kinetic: "Motion graphics / explainer kinetik (teks besar, kartu UI, angka berjalan, potongan cepat)",
  kartun: "Kartun 2D flat (karakter & benda SVG sederhana yang bergerak, tetap dengan teks kinetik)",
};

export const SUARA: { id: string; label: string }[] = [
  { id: "id-ID-GadisNeural", label: "Gadis (perempuan)" },
  { id: "id-ID-ArdiNeural", label: "Ardi (laki-laki)" },
];

export const size = (r: Rasio) => (r === "16:9" ? { width: 1920, height: 1080 } : { width: 1080, height: 1920 });

/** Kecepatan bicara TTS terukur: ±1,5-1,85 kata/detik tergantung panjang kata; pakai angka aman. */
export const WORDS_PER_SEC = 1.6;
/** Batas kata narasi agar durasi mendekati target. */
export const maxWords = (durasi: number) => Math.round(Math.max(15, durasi) * WORDS_PER_SEC);
export const sceneRange = (durasi: number) => {
  const n = Math.round(durasi / 7);
  return { min: Math.max(3, n - 1), max: Math.max(4, Math.min(16, n + 1)) };
};

export function normalizeConfig(c: Partial<VideoConfig>): VideoConfig {
  const d = { ...DEFAULT_CONFIG, ...c };
  return {
    ...d,
    topik: String(d.topik ?? "").trim(),
    judul: d.judul?.trim() || undefined,
    durasi: Math.min(300, Math.max(15, Number(d.durasi) || 45)),
    rasio: d.rasio === "16:9" ? "16:9" : "9:16",
    gaya: d.gaya === "kartun" ? "kartun" : "kinetic",
    suara: SUARA.some((s) => s.id === d.suara) ? d.suara : DEFAULT_CONFIG.suara,
    catatan: d.catatan?.trim() || undefined,
  };
}
