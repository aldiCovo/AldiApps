import fs from "node:fs";
import { MsEdgeTTS, OUTPUT_FORMAT } from "msedge-tts";
import { env } from "./env";

const BITRATE = 96000; // MP3 CBR, jadi durasi = ukuran file * 8 / bitrate

const escapeXml = (s: string) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&apos;");

/** Kata yang diucapkan, waktu dalam detik dari awal audio. */
export type TimedWord = { text: string; start: number; end: number };

// Metadata WordBoundary Edge TTS: Offset & Duration dalam satuan 100 nanodetik.
function parseWordBoundaries(raw: string): TimedWord[] {
  const re = /"Offset"\s*:\s*(\d+)\s*,\s*"Duration"\s*:\s*(\d+)\s*,\s*"text"\s*:\s*\{\s*"Text"\s*:\s*"((?:[^"\\]|\\.)*)"/g;
  const words: TimedWord[] = [];
  for (const m of raw.matchAll(re)) {
    let text = m[3];
    try {
      text = JSON.parse(`"${m[3]}"`);
    } catch {
      /* biarkan apa adanya */
    }
    const start = Number(m[1]) / 1e7;
    words.push({ text, start, end: start + Number(m[2]) / 1e7 });
  }
  return words.sort((a, b) => a.start - b.start);
}

/** Buat file mp3 narasi. Kembalikan durasi (detik) + timing per kata. */
export async function synthesize(text: string, outFile: string, voice?: string): Promise<{ seconds: number; words: TimedWord[] }> {
  const tts = new MsEdgeTTS();
  try {
    await tts.setMetadata(voice || env("TTS_VOICE", "id-ID-GadisNeural"), OUTPUT_FORMAT.AUDIO_24KHZ_96KBITRATE_MONO_MP3, {
      wordBoundaryEnabled: true,
    });
    const { audioStream, metadataStream } = tts.toStream(escapeXml(text), { rate: env("TTS_RATE", "+10%") });
    const chunks: Buffer[] = [];
    let meta = "";
    const metaDone = new Promise<void>((resolve) => {
      if (!metadataStream) return resolve();
      metadataStream.on("data", (d: Buffer) => (meta += d.toString()));
      metadataStream.on("end", () => resolve());
      metadataStream.on("close", () => resolve());
      setTimeout(resolve, 15000);
    });
    await new Promise<void>((resolve, reject) => {
      audioStream.on("data", (d: Buffer) => chunks.push(d));
      audioStream.on("close", () => resolve());
      audioStream.on("end", () => resolve());
      audioStream.on("error", reject);
    });
    await Promise.race([metaDone, new Promise((r) => setTimeout(r, 1500))]);
    const buf = Buffer.concat(chunks);
    if (buf.length < 1000) throw new Error("Audio TTS kosong, cek koneksi internet.");
    fs.writeFileSync(outFile, buf);
    return { seconds: (buf.length * 8) / BITRATE, words: parseWordBoundaries(meta) };
  } finally {
    tts.close();
  }
}
