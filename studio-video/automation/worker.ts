// Laptop render (worker): mengambil pekerjaan render dari Studio di server, merender di komputer ini,
// lalu mengirim progres dan video MP4 kembali ke server.
//
// Pengaturan di .env (komputer worker):
//   STUDIO_URL=https://studio.aldiab.ai.id   (alamat Studio di server)
//   WORKER_TOKEN=...                          (sama persis dengan WORKER_TOKEN di server)
//   WORKER_NAME=laptop-i7                     (opsional, nama yang tampil di Studio)
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import type { JobData } from "../src/auto/types";
import { render } from "./build";
import { env, ROOT } from "./env";
import { ensureSfx } from "./sfx";

const URL_BASE = env("STUDIO_URL", "http://127.0.0.1:4321").replace(/\/+$/, "");
const TOKEN = env("WORKER_TOKEN");
const NAME = env("WORKER_NAME", os.hostname());
const POLL_MS = 5000;
const CHUNK = 8 * 1024 * 1024; // 8 MB per kiriman (di bawah batas 100 MB Cloudflare)

// Folder kerja terpisah agar tidak bentrok dengan Studio lokal yang memakai src/generated.
const WORK = path.join(ROOT, ".worker");
const WORK_SRC = path.join(WORK, "src");
const PUBLIC = path.join(ROOT, "public");

type Claim = { renderId: string; jobId: string; job: JobData; code: string; files: string[] };

const log = (m: string) => console.log(`${new Date().toLocaleTimeString("id-ID")}  ${m}`);
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const headers = (extra: Record<string, string> = {}) => ({ Authorization: `Bearer ${TOKEN}`, "X-Worker-Name": NAME, ...extra });

async function post(pathname: string, body: unknown): Promise<Response> {
  return fetch(URL_BASE + pathname, {
    method: "POST",
    headers: headers({ "Content-Type": "application/json" }),
    body: JSON.stringify(body),
  });
}

/** Unduh file narasi/musik yang dibutuhkan ke public/ (dilewati jika sudah ada dengan ukuran sama). */
async function fetchFiles(files: string[]) {
  for (const rel of files) {
    const dest = path.resolve(PUBLIC, rel);
    if (!dest.startsWith(PUBLIC + path.sep)) throw new Error(`Path file tidak valid: ${rel}`);
    const res = await fetch(`${URL_BASE}/api/worker/file?path=${encodeURIComponent(rel)}`, { headers: headers() });
    if (!res.ok) throw new Error(`Gagal mengunduh ${rel}: HTTP ${res.status}`);
    const buf = Buffer.from(await res.arrayBuffer());
    fs.mkdirSync(path.dirname(dest), { recursive: true });
    fs.writeFileSync(dest, buf);
  }
}

/** Salin src/ ke .worker/src lalu tulis kode job ke .worker/src/generated. */
function prepareSource(job: JobData, code: string) {
  fs.rmSync(WORK_SRC, { recursive: true, force: true });
  fs.cpSync(path.join(ROOT, "src"), WORK_SRC, {
    recursive: true,
    filter: (src) => !src.includes(`${path.sep}generated${path.sep}`),
  });
  const gen = path.join(WORK_SRC, "generated");
  fs.mkdirSync(gen, { recursive: true });
  fs.writeFileSync(
    path.join(gen, "job.ts"),
    `import type { JobData } from "../auto/types";\n\nexport const job: JobData = ${JSON.stringify(job, null, 2)};\n`,
  );
  fs.writeFileSync(path.join(gen, "Scenes.tsx"), code);
}

/** Kirim MP4 dalam potongan; bisa melanjutkan jika koneksi putus di tengah. */
async function uploadVideo(renderId: string, file: string) {
  const size = fs.statSync(file).size;
  const fd = fs.openSync(file, "r");
  try {
    let offset = 0;
    let retries = 0;
    while (offset < size) {
      const len = Math.min(CHUNK, size - offset);
      const buf = Buffer.alloc(len);
      fs.readSync(fd, buf, 0, len, offset);
      try {
        const res = await fetch(`${URL_BASE}/api/worker/upload/${renderId}`, {
          method: "POST",
          headers: headers({ "Content-Type": "application/octet-stream", "X-Offset": String(offset) }),
          body: buf,
        });
        const j = (await res.json().catch(() => ({}))) as { size?: number; error?: string };
        if (res.status === 404) throw new Error("Server tidak lagi mengenali render ini.");
        if (typeof j.size !== "number") throw new Error(j.error ?? `HTTP ${res.status}`);
        offset = j.size; // 200: lanjut; 409: server minta lanjut dari ukuran yang ia punya
        retries = 0;
        process.stdout.write(`\r  Upload ${((offset / size) * 100).toFixed(0)}%   `);
      } catch (e) {
        if ((e as Error).message.includes("tidak lagi mengenali") || ++retries > 5) throw e;
        log(`Upload terputus (${(e as Error).message}), coba lagi...`);
        await sleep(3000 * retries);
      }
    }
    process.stdout.write("\n");
  } finally {
    fs.closeSync(fd);
  }
  const res = await post(`/api/worker/complete/${renderId}`, { size });
  if (!res.ok) throw new Error(`Server menolak hasil: ${((await res.json().catch(() => ({}))) as { error?: string }).error ?? res.status}`);
}

async function handle(c: Claim) {
  log(`Pekerjaan baru: ${c.jobId}`);
  const say = (m: string) => post(`/api/worker/progress/${c.renderId}`, { log: m }).catch(() => undefined);
  const outFile = path.join(WORK, "out", `${c.renderId}.mp4`);
  try {
    await say("Mengunduh file narasi...");
    await fetchFiles(c.files);
    ensureSfx(PUBLIC);
    prepareSource(c.job, c.code);
    fs.mkdirSync(path.dirname(outFile), { recursive: true });
    await say("Merender...");
    let last = 0;
    const error = await render(outFile, (stage, done, total) => {
      const now = Date.now();
      if (now - last < 1000 && done !== total) return;
      last = now;
      process.stdout.write(`\r  ${stage === "render" ? "Render" : "Encode"} ${done}/${total}   `);
      post(`/api/worker/progress/${c.renderId}`, { stage, done, total }).catch(() => undefined);
    }, path.relative(ROOT, path.join(WORK_SRC, "index.ts")));
    process.stdout.write("\n");
    if (error) {
      log("Render gagal, error dikirim ke server.");
      await post(`/api/worker/fail/${c.renderId}`, { error });
      return;
    }
    await say("Render selesai, mengirim video ke server...");
    await uploadVideo(c.renderId, outFile);
    log("Selesai dan terkirim.");
  } catch (e) {
    log(`Gagal: ${(e as Error).message}`);
    await post(`/api/worker/fail/${c.renderId}`, { error: `Laptop render: ${(e as Error).message}` }).catch(() => undefined);
  } finally {
    fs.rmSync(outFile, { force: true });
  }
}

async function main() {
  if (!TOKEN || TOKEN.length < 16) {
    console.error("Isi WORKER_TOKEN di .env (minimal 16 karakter, sama dengan di server).");
    process.exit(1);
  }
  log(`Laptop render "${NAME}" terhubung ke ${URL_BASE}. Menunggu pekerjaan... (Ctrl+C untuk berhenti)`);
  let warned = false;
  for (;;) {
    try {
      const res = await fetch(`${URL_BASE}/api/worker/next`, { method: "POST", headers: headers() });
      if (res.status === 401) {
        if (!warned) log("Token ditolak server. Cek WORKER_TOKEN di kedua komputer.");
        warned = true;
      } else if (res.status === 200) {
        warned = false;
        await handle((await res.json()) as Claim);
        continue; // langsung cek pekerjaan berikutnya
      } else if (res.status !== 204) {
        log(`Server menjawab HTTP ${res.status}`);
      } else warned = false;
    } catch (e) {
      if (!warned) log(`Server belum bisa dihubungi (${(e as Error).message}). Mencoba terus...`);
      warned = true;
    }
    await sleep(POLL_MS);
  }
}

main();
