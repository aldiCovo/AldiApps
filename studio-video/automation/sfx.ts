import fs from "node:fs";
import path from "node:path";

// Efek suara dibuat secara sintetis (tanpa unduhan): whoosh, pop, tick -> public/sfx/*.wav
const SR = 44100;

function toWav(samples: Float32Array): Buffer {
  let peak = 0;
  for (const s of samples) peak = Math.max(peak, Math.abs(s));
  const gain = peak > 0 ? 0.8 / peak : 1;
  const buf = Buffer.alloc(44 + samples.length * 2);
  buf.write("RIFF", 0);
  buf.writeUInt32LE(36 + samples.length * 2, 4);
  buf.write("WAVE", 8);
  buf.write("fmt ", 12);
  buf.writeUInt32LE(16, 16);
  buf.writeUInt16LE(1, 20); // PCM
  buf.writeUInt16LE(1, 22); // mono
  buf.writeUInt32LE(SR, 24);
  buf.writeUInt32LE(SR * 2, 28);
  buf.writeUInt16LE(2, 32);
  buf.writeUInt16LE(16, 34);
  buf.write("data", 36);
  buf.writeUInt32LE(samples.length * 2, 40);
  samples.forEach((s, i) => buf.writeInt16LE(Math.round(Math.max(-1, Math.min(1, s * gain)) * 32767), 44 + i * 2));
  return buf;
}

// Pseudo-random deterministik.
function lcg(seed: number) {
  let x = seed >>> 0;
  return () => ((x = (1664525 * x + 1013904223) >>> 0) / 4294967296) * 2 - 1;
}

/** Noise yang disaring band-pass dengan frekuensi tengah menyapu naik lalu turun. */
function whoosh(): Float32Array {
  const dur = 0.5;
  const n = Math.floor(SR * dur);
  const out = new Float32Array(n);
  const rnd = lcg(7);
  let low = 0;
  let band = 0;
  for (let i = 0; i < n; i++) {
    const t = i / n;
    const fc = 250 + 3200 * Math.sin(Math.PI * Math.pow(t, 0.8));
    const f = 2 * Math.sin((Math.PI * fc) / SR);
    const x = rnd();
    low += f * band;
    const high = x - low - 0.9 * band;
    band += f * high;
    const env = t < 0.4 ? Math.pow(t / 0.4, 2) : Math.pow(1 - (t - 0.4) / 0.6, 2.2);
    out[i] = band * env;
  }
  return out;
}

/** Sinus pendek yang nadanya turun cepat (pop/bubble). */
function pop(): Float32Array {
  const dur = 0.12;
  const n = Math.floor(SR * dur);
  const out = new Float32Array(n);
  let ph = 0;
  for (let i = 0; i < n; i++) {
    const t = i / SR;
    const freq = 260 + 900 * Math.exp(-t * 40);
    ph += (2 * Math.PI * freq) / SR;
    out[i] = Math.sin(ph) * Math.exp(-t * 32) * Math.min(1, i / 60);
  }
  return out;
}

/** Klik pendek bernada tinggi. */
function tick(): Float32Array {
  const dur = 0.05;
  const n = Math.floor(SR * dur);
  const out = new Float32Array(n);
  for (let i = 0; i < n; i++) {
    const t = i / SR;
    out[i] = (Math.sin(2 * Math.PI * 2400 * t) * 0.7 + Math.sin(2 * Math.PI * 3800 * t) * 0.3) * Math.exp(-t * 120);
  }
  return out;
}

export function ensureSfx(publicDir: string): void {
  const dir = path.join(publicDir, "sfx");
  fs.mkdirSync(dir, { recursive: true });
  const files: Record<string, () => Float32Array> = { whoosh, pop, tick };
  for (const [name, gen] of Object.entries(files)) {
    const file = path.join(dir, `${name}.wav`);
    if (!fs.existsSync(file)) fs.writeFileSync(file, toWav(gen()));
  }
}
