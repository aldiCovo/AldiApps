// Antrean render jarak jauh: server memasukkan pekerjaan, worker (laptop render) mengambil,
// merender, mengirim progres, lalu mengupload MP4 dalam potongan.
import { randomUUID, timingSafeEqual } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import type { JobData } from "../src/auto/types";
import { env } from "./env";
import type { Reporter } from "./pipeline";

type Status = "waiting" | "claimed";
type RenderItem = {
  renderId: string;
  jobId: string;
  job: JobData;
  code: string;
  outFile: string;
  partFile: string;
  status: Status;
  worker: string | null;
  lastSeen: number;
  reporter: Reporter;
  finish: (error: string | null) => void;
};

const STALE_MS = 90_000; // worker diam lebih dari ini -> pekerjaan dikembalikan ke antrean
const items = new Map<string, RenderItem>();
const workers = new Map<string, number>(); // nama -> terakhir terlihat

export const remoteRenderEnabled = () => env("RENDER_MODE") === "remote";

export function checkWorkerAuth(header: string | undefined): boolean {
  const token = env("WORKER_TOKEN");
  if (!token || token.length < 16) return false;
  const got = Buffer.from((header ?? "").replace(/^Bearer\s+/i, ""));
  const want = Buffer.from(token);
  return got.length === want.length && timingSafeEqual(got, want);
}

export function workerStatus() {
  const now = Date.now();
  const list = [...workers.entries()].map(([name, t]) => ({ name, online: now - t < 20_000, lastSeen: new Date(t).toISOString() }));
  return {
    mode: remoteRenderEnabled() ? "remote" : "local",
    workers: list,
    online: list.some((w) => w.online),
    queue: [...items.values()].map((i) => ({ jobId: i.jobId, status: i.status, worker: i.worker })),
  };
}

function requeueStale() {
  const now = Date.now();
  for (const it of items.values()) {
    if (it.status === "claimed" && now - it.lastSeen > STALE_MS) {
      it.reporter.log(`Laptop render "${it.worker}" tidak merespons, pekerjaan dikembalikan ke antrean.`);
      it.status = "waiting";
      it.worker = null;
      fs.rmSync(it.partFile, { force: true });
    }
  }
}
setInterval(requeueStale, 15_000).unref();

/** Masukkan render ke antrean dan tunggu hasilnya. Mengembalikan null jika sukses, atau pesan error render. */
export function renderRemote(job: JobData, code: string, outFile: string, reporter: Reporter): Promise<string | null> {
  return new Promise((resolve) => {
    const renderId = randomUUID();
    const item: RenderItem = {
      renderId,
      jobId: job.id,
      job,
      code,
      outFile,
      partFile: `${outFile}.part`,
      status: "waiting",
      worker: null,
      lastSeen: Date.now(),
      reporter,
      finish: (error) => {
        items.delete(renderId);
        resolve(error);
      },
    };
    fs.rmSync(item.partFile, { force: true });
    items.set(renderId, item);
    const online = workerStatus().online;
    reporter.log(online ? "Dikirim ke laptop render..." : "Menunggu laptop render online (pekerjaan aman di antrean)...");
  });
}

/* ---------------- dipanggil oleh endpoint worker ---------------- */

/** File di public/ yang dibutuhkan worker untuk merender job ini. */
const neededFiles = (job: JobData) =>
  [...job.scenes.map((s) => s.audio), job.music].filter((x): x is string => Boolean(x));

export function claimNext(worker: string) {
  workers.set(worker, Date.now());
  requeueStale();
  const it = [...items.values()].find((i) => i.status === "waiting");
  if (!it) return null;
  it.status = "claimed";
  it.worker = worker;
  it.lastSeen = Date.now();
  it.reporter.log(`Laptop render "${worker}" mulai merender.`);
  return { renderId: it.renderId, jobId: it.jobId, job: it.job, code: it.code, files: neededFiles(it.job) };
}

const get = (renderId: string, worker: string) => {
  const it = items.get(renderId);
  if (!it || it.worker !== worker) return null;
  it.lastSeen = Date.now();
  workers.set(worker, Date.now());
  return it;
};

export function isNeededFile(rel: string): boolean {
  return [...items.values()].some((i) => i.status === "claimed" && neededFiles(i.job).includes(rel));
}

export function reportProgress(renderId: string, worker: string, p: { stage?: string; done?: number; total?: number; log?: string }) {
  const it = get(renderId, worker);
  if (!it) return false;
  if (p.log) it.reporter.log(`[laptop] ${p.log}`);
  if (p.stage && typeof p.done === "number" && typeof p.total === "number")
    it.reporter.progress?.(p.stage === "encode" ? "encode" : "render", p.done, p.total);
  return true;
}

/**
 * Tambahkan potongan MP4 pada `offset`. ok=false berarti offset tidak cocok dan worker harus melanjutkan dari `size`.
 * Mengembalikan null jika render tidak dikenal.
 */
export function appendChunk(renderId: string, worker: string, offset: number, data: Buffer): { ok: boolean; size: number } | null {
  const it = get(renderId, worker);
  if (!it) return null;
  const size = fs.existsSync(it.partFile) ? fs.statSync(it.partFile).size : 0;
  if (offset === size) {
    fs.appendFileSync(it.partFile, data);
    return { ok: true, size: size + data.length };
  }
  if (offset + data.length <= size) return { ok: true, size }; // potongan ini sudah diterima (kiriman ulang)
  return { ok: false, size };
}

export function complete(renderId: string, worker: string, size: number): string | null {
  const it = get(renderId, worker);
  if (!it) return "Render tidak dikenal";
  const got = fs.existsSync(it.partFile) ? fs.statSync(it.partFile).size : -1;
  if (got !== size) return `Ukuran tidak cocok (server ${got}, laptop ${size})`;
  fs.mkdirSync(path.dirname(it.outFile), { recursive: true });
  fs.renameSync(it.partFile, it.outFile);
  it.reporter.log(`Video diterima dari laptop (${(size / 1e6).toFixed(1)} MB).`);
  it.finish(null);
  return null;
}

export function fail(renderId: string, worker: string, error: string): boolean {
  const it = get(renderId, worker);
  if (!it) return false;
  fs.rmSync(it.partFile, { force: true });
  it.finish(error || "Render gagal di laptop (tanpa pesan).");
  return true;
}
