// Mesin produksi video yang dipakai bersama oleh CLI (cli.ts) dan Studio web (server.ts).
import fs from "node:fs";
import path from "node:path";
import type { JobData, SceneWord } from "../src/auto/types";
import { render, typecheck, writeGenerated, type RenderProgress } from "./build";
import { fixSceneCode, reviseSceneCode, writeSceneCode, type CodeSession, type Plan } from "./claude";
import { DEFAULT_CONFIG, normalizeConfig, size, type VideoConfig } from "./config";
import { ROOT } from "./env";
import { remoteRenderEnabled, renderRemote } from "./queue";
import { ensureSfx } from "./sfx";
import { synthesize } from "./tts";
import { facebookReady, instagramReady, uploadFacebook, uploadInstagram } from "./upload/meta";
import { tiktokReady, uploadTikTok } from "./upload/tiktok";
import type { UploadInput } from "./upload/types";
import { uploadYouTube, youtubeReady } from "./upload/youtube";
import type { AiMode } from "./claude";
import { DATA_DIR } from "./users";
import { monthOf, PRE_LANGKAH, readLedger, readUsage, recorder, renderEntry, summarize, type OnUsage, type UsageEntry, type UsageTotal } from "./usage";

export const FPS = 30;
const MAX_FIX = 4;
export const JOBS_DIR = path.join(ROOT, "jobs");

/** Penerima laporan kemajuan (terminal atau browser). */
export type Reporter = {
  log: (msg: string) => void;
  stage?: (stage: Stage) => void;
  progress?: RenderProgress;
};
export type Stage = "suara" | "kode" | "cek" | "render" | "perbaikan" | "selesai";

const slugify = (s: string) =>
  s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 40) || "video";

const stamp = () => {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
};

export const jobDirOf = (id: string) => (path.isAbsolute(id) ? id : path.join(JOBS_DIR, path.basename(id)));

/* ---------------- Subtitle SRT ---------------- */

const srtTime = (sec: number) => {
  const ms = Math.max(0, Math.round(sec * 1000));
  const p = (n: number, l = 2) => String(n).padStart(l, "0");
  return `${p(Math.floor(ms / 3600000))}:${p(Math.floor(ms / 60000) % 60)}:${p(Math.floor(ms / 1000) % 60)},${p(ms % 1000, 3)}`;
};

/** Tulis subtitles.srt dari timing kata (maks 7 kata per baris, pecah di jeda bicara). */
export function writeSrt(job: JobData, file: string): void {
  const cues: string[] = [];
  let offset = 0;
  let n = 1;
  for (const sc of job.scenes) {
    const words = sc.words ?? [];
    let cur: SceneWord[] = [];
    const flush = () => {
      if (!cur.length) return;
      const start = (offset + cur[0].s) / job.fps;
      const end = (offset + Math.max(cur[cur.length - 1].e, cur[0].s + 10)) / job.fps;
      cues.push(`${n++}\n${srtTime(start)} --> ${srtTime(end)}\n${cur.map((w) => w.t).join(" ")}\n`);
      cur = [];
    };
    for (const w of words) {
      const prev = cur[cur.length - 1];
      if (prev && (cur.length >= 7 || (w.s - prev.e) / job.fps > 0.3)) flush();
      cur.push(w);
    }
    flush();
    offset += sc.durationInFrames;
  }
  fs.writeFileSync(file, cues.join("\n"));
}

/* ---------------- Produksi ---------------- */

/**
 * Tulis kode -> cek tipe -> render (lokal, atau lewat laptop render jika RENDER_MODE=remote).
 * Kalau gagal dan ada sesi Claude, error dikirim ke Claude untuk diperbaiki.
 */
async function buildVideo(
  job: JobData,
  code: string,
  session: CodeSession | null,
  outFile: string,
  r: Reporter,
  onUsage?: OnUsage,
): Promise<string> {
  const remote = remoteRenderEnabled();
  const max = session ? MAX_FIX : 1;
  for (let attempt = 1; attempt <= max; attempt++) {
    writeGenerated(job, code);
    r.stage?.("cek");
    r.log(`[${attempt}/${max}] Cek kode...`);
    let error = await typecheck();
    if (!error) {
      r.stage?.("render");
      r.log(`[${attempt}/${max}] Render video ${remote ? "di laptop render" : "di komputer ini"}...`);
      const started = Date.now();
      if (remote) {
        const out = await renderRemote(job, code, outFile, r);
        error = out.error;
        onUsage?.(renderEntry(out.seconds));
      } else {
        error = await render(outFile, r.progress);
        onUsage?.(renderEntry((Date.now() - started) / 1000));
      }
    }
    if (!error) return code;
    if (!session || attempt === max) throw new Error(`Gagal setelah ${attempt} percobaan. Error terakhir:\n${error.slice(-2000)}`);
    r.stage?.("perbaikan");
    r.log("Ada error, Claude memperbaiki kode...");
    code = await fixSceneCode(session, error, onUsage);
  }
  throw new Error("unreachable");
}

const saveSession = (jobDir: string, s: CodeSession) => fs.writeFileSync(path.join(jobDir, "session.json"), JSON.stringify(s));

/**
 * Produksi lengkap: suara -> kode -> cek -> render. Mengembalikan id job.
 * `preUsage` = biaya langkah prompt & storyboard, yang dijalankan sebelum job ada.
 */
export async function produce(
  plan: Plan,
  prompt: string,
  cfgIn: Partial<VideoConfig>,
  r: Reporter,
  preUsage: UsageEntry[] = [],
  ownerId: string | null = null, // openId pemilik; null = pemilik Studio (CLI, video lama)
  onCreated?: (id: string) => void,
  mode?: AiMode, // kosong = DEFAULT_MODE (CLI/laptop)
): Promise<string> {
  const cfg = normalizeConfig({ ...DEFAULT_CONFIG, ...cfgIn });
  const id = `${stamp()}-${slugify(plan.judul)}`;
  const jobDir = jobDirOf(id);
  const audioDir = path.join(ROOT, "public", "jobs", id);
  fs.mkdirSync(jobDir, { recursive: true });
  fs.mkdirSync(audioDir, { recursive: true });
  if (ownerId) fs.writeFileSync(path.join(jobDir, "owner.json"), JSON.stringify({ openId: ownerId }));
  onCreated?.(id);
  fs.writeFileSync(path.join(jobDir, "prompt.txt"), prompt);
  fs.writeFileSync(path.join(jobDir, "config.json"), JSON.stringify(cfg, null, 2));
  fs.writeFileSync(path.join(jobDir, "plan.json"), JSON.stringify(plan, null, 2));
  fs.writeFileSync(path.join(jobDir, "storyboard.json"), JSON.stringify({ config: cfg, ...plan }, null, 2));
  const track = recorder(jobDir);
  preUsage.forEach(track);

  r.stage?.("suara");
  r.log("Membuat suara narasi + timing per kata...");
  const durations: number[] = [];
  const sceneWords: SceneWord[][] = [];
  for (const [i, s] of plan.scenes.entries()) {
    const { seconds, words } = await synthesize(s.narasi, path.join(audioDir, `s${i + 1}.mp3`), cfg.suara);
    durations.push(Math.ceil((seconds + 0.35) * FPS));
    sceneWords.push(words.map((w) => ({ t: w.text, s: Math.round(w.start * FPS), e: Math.round(w.end * FPS) })));
    r.log(`  Scene ${i + 1}: ${seconds.toFixed(1)} detik`);
  }
  ensureSfx(path.join(ROOT, "public"));
  r.log(`Durasi total: ${(durations.reduce((a, b) => a + b, 0) / FPS).toFixed(1)} detik`);

  const musicDir = path.join(ROOT, "public", "music");
  const tracks = fs.existsSync(musicDir) ? fs.readdirSync(musicDir).filter((f) => /\.(mp3|m4a|wav)$/i.test(f)) : [];
  const { width, height } = size(cfg.rasio);
  const job: JobData = {
    id,
    fps: FPS,
    width,
    height,
    music: tracks.length ? `music/${tracks[Math.floor(Math.random() * tracks.length)]}` : null,
    sfx: true,
    scenes: plan.scenes.map((s, i) => ({
      narasi: s.narasi,
      durationInFrames: durations[i],
      audio: `jobs/${id}/s${i + 1}.mp3`,
      words: sceneWords[i],
    })),
  };
  fs.writeFileSync(path.join(jobDir, "job.json"), JSON.stringify(job, null, 2));
  writeSrt(job, path.join(jobDir, "subtitles.srt"));

  r.stage?.("kode");
  r.log("Claude menulis kode animasi (biasanya 5-10 menit)...");
  const { code: first, session } = await writeSceneCode(plan, durations, sceneWords, cfg, track, mode);
  saveSession(jobDir, session);
  const code = await buildVideo(job, first, session, path.join(jobDir, "video.mp4"), r, (u) => {
    track(u);
    saveSession(jobDir, session); // simpan cliCostUsd agar perbaikan berikutnya tidak terhitung dua kali
  });
  saveSession(jobDir, session);
  fs.writeFileSync(path.join(jobDir, "Scenes.tsx"), code);
  r.stage?.("selesai");
  r.log("Video selesai.");
  return id;
}

/** Revisi animasi job yang sudah ada berdasarkan masukan pengguna, lalu render ulang. */
export async function revise(id: string, feedback: string, r: Reporter): Promise<void> {
  const jobDir = jobDirOf(id);
  const job: JobData = JSON.parse(fs.readFileSync(path.join(jobDir, "job.json"), "utf8"));
  const sessFile = path.join(jobDir, "session.json");
  if (!fs.existsSync(sessFile)) throw new Error("Sesi Claude untuk video ini tidak tersimpan, jadi tidak bisa direvisi. Buat ulang videonya.");
  const session: CodeSession = JSON.parse(fs.readFileSync(sessFile, "utf8"));
  r.stage?.("kode");
  r.log("Claude merevisi kode animasi...");
  const track = recorder(jobDir);
  const draft = await reviseSceneCode(session, feedback, track);
  saveSession(jobDir, session);
  const code = await buildVideo(job, draft, session, path.join(jobDir, "video.mp4"), r, (u) => {
    track(u);
    saveSession(jobDir, session);
  });
  saveSession(jobDir, session);
  fs.writeFileSync(path.join(jobDir, "Scenes.tsx"), code);
  fs.appendFileSync(path.join(jobDir, "revisi.txt"), `${new Date().toISOString()}  ${feedback}\n`);
  r.stage?.("selesai");
  r.log("Revisi selesai.");
}

/** Render ulang job yang sudah ada dari kode tersimpan (tanpa memanggil Claude). */
export async function rerender(id: string, r: Reporter): Promise<void> {
  const jobDir = jobDirOf(id);
  const jobFile = path.join(jobDir, "job.json");
  const codeFile = path.join(jobDir, "Scenes.tsx");
  if (!fs.existsSync(jobFile) || !fs.existsSync(codeFile)) throw new Error("Job ini tidak punya job.json / Scenes.tsx tersimpan.");
  const job: JobData = JSON.parse(fs.readFileSync(jobFile, "utf8"));
  ensureSfx(path.join(ROOT, "public"));
  r.log("Render ulang dari kode tersimpan...");
  await buildVideo(job, fs.readFileSync(codeFile, "utf8"), null, path.join(jobDir, "video.mp4"), r, recorder(jobDir));
  r.stage?.("selesai");
  r.log("Render ulang selesai.");
}

/* ---------------- Upload ---------------- */

export type Platform = { name: string; ready: () => boolean; upload: (i: UploadInput) => Promise<string> };
export const PLATFORMS: Platform[] = [
  { name: "YouTube", ready: youtubeReady, upload: uploadYouTube },
  { name: "TikTok", ready: tiktokReady, upload: uploadTikTok },
  { name: "Instagram", ready: instagramReady, upload: uploadInstagram },
  { name: "Facebook", ready: facebookReady, upload: uploadFacebook },
];

export async function uploadJob(id: string, names: string[], r: Reporter): Promise<Record<string, string>> {
  const jobDir = jobDirOf(id);
  const plan: Plan = JSON.parse(fs.readFileSync(path.join(jobDir, "plan.json"), "utf8"));
  const input: UploadInput = {
    file: path.join(jobDir, "video.mp4"),
    title: plan.judul,
    description: plan.deskripsi,
    caption: plan.caption,
    tags: plan.tags,
  };
  const resultsPath = path.join(jobDir, "uploads.json");
  const results: Record<string, string> = fs.existsSync(resultsPath) ? JSON.parse(fs.readFileSync(resultsPath, "utf8")) : {};
  for (const p of PLATFORMS.filter((x) => names.includes(x.name))) {
    if (!p.ready()) {
      results[p.name] = "GAGAL: belum dikonfigurasi (lihat README)";
      r.log(`${p.name}: belum dikonfigurasi`);
      continue;
    }
    r.log(`Upload ke ${p.name}...`);
    try {
      results[p.name] = await p.upload(input);
      r.log(`${p.name}: OK -> ${results[p.name]}`);
    } catch (e) {
      results[p.name] = `GAGAL: ${(e as Error).message}`;
      r.log(`${p.name}: GAGAL - ${(e as Error).message}`);
    }
  }
  fs.writeFileSync(resultsPath, JSON.stringify(results, null, 2));
  return results;
}

/* ---------------- Riwayat ---------------- */

export type JobSummary = {
  id: string;
  judul: string;
  hasVideo: boolean;
  rasio: string;
  created: string;
  uploads: Record<string, string>;
  biaya: { usd: number; tokens: number; partial: boolean } | null; // null = video lama tanpa catatan biaya
};

const readJson = <T>(file: string, fallback: T): T => {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8")) as T;
  } catch {
    return fallback;
  }
};

/** openId pemilik job, atau null untuk job tanpa owner.json (milik pemilik Studio). */
export function jobOwner(id: string): string | null {
  return readJson<{ openId?: string }>(path.join(jobDirOf(id), "owner.json"), {}).openId ?? null;
}

/** `canSee(ownerId)` menyaring job per pengguna; tanpa filter = semua job. */
export function listJobs(canSee: (ownerId: string | null) => boolean = () => true): JobSummary[] {
  if (!fs.existsSync(JOBS_DIR)) return [];
  return fs
    .readdirSync(JOBS_DIR)
    .filter((d) => !d.startsWith("_") && fs.existsSync(path.join(JOBS_DIR, d, "plan.json")) && canSee(jobOwner(d)))
    .sort()
    .reverse()
    .map((d) => {
      const dir = path.join(JOBS_DIR, d);
      const plan = readJson<Partial<Plan>>(path.join(dir, "plan.json"), {});
      const cfg = readJson<Partial<VideoConfig>>(path.join(dir, "config.json"), {});
      return {
        id: d,
        judul: plan.judul ?? d,
        hasVideo: fs.existsSync(path.join(dir, "video.mp4")),
        rasio: cfg.rasio ?? "9:16",
        created: fs.statSync(dir).mtime.toISOString(),
        uploads: readJson<Record<string, string>>(path.join(dir, "uploads.json"), {}),
        biaya: biayaOf(dir),
      };
    });
}

function biayaOf(dir: string): JobSummary["biaya"] {
  if (!fs.existsSync(path.join(dir, "usage.json"))) return null;
  const t = summarize(readUsage(dir));
  return { usd: t.usd, tokens: t.tokens, partial: t.usdPartial };
}

export function loadJob(id: string) {
  const dir = jobDirOf(id);
  if (!fs.existsSync(path.join(dir, "plan.json"))) return null;
  return {
    id: path.basename(dir),
    plan: readJson<Plan | null>(path.join(dir, "plan.json"), null),
    prompt: fs.existsSync(path.join(dir, "prompt.txt")) ? fs.readFileSync(path.join(dir, "prompt.txt"), "utf8") : "",
    config: readJson<Partial<VideoConfig>>(path.join(dir, "config.json"), {}),
    hasVideo: fs.existsSync(path.join(dir, "video.mp4")),
    hasSrt: fs.existsSync(path.join(dir, "subtitles.srt")),
    canRevise: fs.existsSync(path.join(dir, "session.json")),
    canRerender: fs.existsSync(path.join(dir, "Scenes.tsx")) && fs.existsSync(path.join(dir, "job.json")),
    uploads: readJson<Record<string, string>>(path.join(dir, "uploads.json"), {}),
    biaya: fs.existsSync(path.join(dir, "usage.json")) ? summarize(readUsage(dir)) : null,
  };
}

/* ---------------- Rekap pemakaian per pengguna per bulan ---------------- */

export type TaggedEntry = UsageEntry & { jobId: string | null };

/**
 * Semua catatan pemakaian satu pengguna (semua bulan), dengan sumber yang sama seperti monthlyUsage:
 * ledger prompt/storyboard (jobId null) ditambah langkah job miliknya. Untuk dasbor biaya.
 */
export function usageEntries(openId: string | null, ledgerKey: string): TaggedEntry[] {
  const out: TaggedEntry[] = readLedger(DATA_DIR, ledgerKey).map((e) => ({ ...e, jobId: null }));
  if (fs.existsSync(JOBS_DIR)) {
    for (const d of fs.readdirSync(JOBS_DIR)) {
      if (d.startsWith("_") || jobOwner(d) !== openId) continue;
      for (const e of readUsage(path.join(JOBS_DIR, d))) if (!PRE_LANGKAH.includes(e.langkah)) out.push({ ...e, jobId: d });
    }
  }
  return out.sort((a, b) => a.at.localeCompare(b.at));
}

export type MonthlyUsage = UsageTotal & { videos: number };

/**
 * Pemakaian satu pengguna di bulan (YYYY-MM, WIB): ledger prompt/storyboard dari server, ditambah langkah
 * kode, perbaikan, revisi, dan render dari job miliknya. Langkah prompt/storyboard di usage.json job
 * dilewati, karena berasal dari browser dan sudah tercatat di ledger.
 * `openId` null = pemilik Studio (job tanpa owner.json).
 */
export function monthlyUsage(openId: string | null, ledgerKey: string, month: string): MonthlyUsage {
  const entries: UsageEntry[] = readLedger(DATA_DIR, ledgerKey).filter((e) => monthOf(e.at) === month);
  let videos = 0;
  if (fs.existsSync(JOBS_DIR)) {
    for (const d of fs.readdirSync(JOBS_DIR)) {
      if (d.startsWith("_") || jobOwner(d) !== openId) continue;
      const own = readUsage(path.join(JOBS_DIR, d)).filter((e) => !PRE_LANGKAH.includes(e.langkah) && monthOf(e.at) === month);
      if (own.some((e) => e.langkah === "kode")) videos++;
      entries.push(...own);
    }
  }
  return { ...summarize(entries), videos };
}
