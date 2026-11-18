// Biaya AI per video: setiap panggilan Claude dicatat (token + perkiraan USD) ke jobs/<id>/usage.json.
import fs from "node:fs";
import path from "node:path";

export type Langkah = "prompt" | "storyboard" | "kode" | "perbaikan" | "revisi";
export const LANGKAH: Langkah[] = ["prompt", "storyboard", "kode", "perbaikan", "revisi"];

export type UsageEntry = {
  langkah: Langkah;
  model: string;
  input: number; // token input tanpa cache
  output: number; // token output (termasuk thinking)
  cacheRead: number;
  cacheWrite: number;
  usd: number | null; // null jika harga model tidak diketahui
  at: string;
};
export type OnUsage = (u: UsageEntry) => void;

export type UsageTotal = {
  calls: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  tokens: number;
  usd: number;
  usdPartial: boolean; // ada panggilan tanpa harga, jadi usd terlalu rendah
  perLangkah: Partial<Record<Langkah, { calls: number; tokens: number; usd: number }>>;
};

// Harga Claude API per 1 juta token (USD). Cache write dihitung 1,25x input (TTL 5 menit, default API).
const PRICE: Record<string, { in: number; out: number; read: number }> = {
  "claude-opus-5-5": { in: 4, out: 20, read: 0.2 },
  "claude-sonnet-5-5": { in: 2, out: 10, read: 0.1 },
  "claude-haiku-5-5": { in: 0.1, out: 0.5, read: 0.01 },
  "claude-fable-5-2": { in: 8, out: 40, read: 0.2 },
};

export function priceUsd(model: string, t: { input: number; output: number; cacheRead: number; cacheWrite: number }): number | null {
  const p = PRICE[model];
  if (!p) return null;
  return (t.input * p.in + t.output * p.out + t.cacheRead * p.read + t.cacheWrite * p.in * 1.25) / 1e6;
}

const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : 0);

/** Bersihkan entri yang datang dari browser (langkah prompt/storyboard terjadi sebelum job dibuat). */
export function cleanEntries(raw: unknown): UsageEntry[] {
  if (!Array.isArray(raw)) return [];
  return raw.slice(0, 50).flatMap((e): UsageEntry[] => {
    if (!e || typeof e !== "object") return [];
    const o = e as Record<string, unknown>;
    if (!LANGKAH.includes(o.langkah as Langkah)) return [];
    return [{
      langkah: o.langkah as Langkah,
      model: String(o.model ?? "").slice(0, 60),
      input: num(o.input),
      output: num(o.output),
      cacheRead: num(o.cacheRead),
      cacheWrite: num(o.cacheWrite),
      usd: o.usd === null ? null : num(o.usd),
      at: String(o.at ?? "").slice(0, 40),
    }];
  });
}

const fileOf = (jobDir: string) => path.join(jobDir, "usage.json");

export function readUsage(jobDir: string): UsageEntry[] {
  try {
    return cleanEntries(JSON.parse(fs.readFileSync(fileOf(jobDir), "utf8")));
  } catch {
    return [];
  }
}

/** Penerima catatan untuk satu job: setiap panggilan langsung ditulis, jadi percobaan yang gagal tetap terhitung. */
export function recorder(jobDir: string): OnUsage {
  return (u) => fs.writeFileSync(fileOf(jobDir), JSON.stringify([...readUsage(jobDir), u], null, 2));
}

export function summarize(entries: UsageEntry[]): UsageTotal {
  const t: UsageTotal = { calls: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0, tokens: 0, usd: 0, usdPartial: false, perLangkah: {} };
  for (const e of entries) {
    const tokens = e.input + e.output + e.cacheRead + e.cacheWrite;
    t.calls++;
    t.input += e.input;
    t.output += e.output;
    t.cacheRead += e.cacheRead;
    t.cacheWrite += e.cacheWrite;
    t.tokens += tokens;
    t.usd += e.usd ?? 0;
    if (e.usd === null) t.usdPartial = true;
    const l = (t.perLangkah[e.langkah] ??= { calls: 0, tokens: 0, usd: 0 });
    l.calls++;
    l.tokens += tokens;
    l.usd += e.usd ?? 0;
  }
  return t;
}
