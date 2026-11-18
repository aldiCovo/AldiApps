// Studio Video: aplikasi web lokal (http://localhost:4321) untuk alur
// Gagasan -> Master Prompt -> Storyboard -> Kode + Render -> Preview -> Revisi / Upload.
import { spawn } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { makeMasterPrompt, makePlan, PlanSchema, USE_API } from "./claude";
import { DEFAULT_CONFIG, GAYA_LABEL, normalizeConfig, SUARA } from "./config";
import { env, ROOT } from "./env";
import { jobDirOf, listJobs, loadJob, PLATFORMS, produce, rerender, revise, uploadJob, type Reporter, type Stage } from "./pipeline";
import { cleanEntries, type UsageEntry } from "./usage";
import { tiktokAuthorizeUrl, tiktokExchangeCode, tiktokKeys } from "./upload/tiktok";
import { appendChunk, checkWorkerAuth, claimNext, complete, fail, isNeededFile, reportProgress, workerStatus } from "./queue";

const PORT = Number(process.env.STUDIO_PORT ?? 4321);
// Default hanya bisa diakses dari komputer ini. Untuk VPN (mis. Tailscale), isi STUDIO_HOST dengan IP VPN server.
const HOST = process.env.STUDIO_HOST?.trim() || "127.0.0.1";
const WEB = path.join(ROOT, "automation", "web");

/* ---------------- Status tugas berjalan (satu per satu, karena render memakai src/generated bersama) ---------------- */

type Task = {
  kind: "produksi" | "revisi" | "upload";
  jobId: string | null;
  stage: Stage | null;
  logs: string[];
  progress: { stage: string; done: number; total: number } | null;
  error: string | null;
  done: boolean;
  startedAt: number;
};
let task: Task | null = null;
const clients = new Set<http.ServerResponse>();

const send = (event: object) => {
  const data = `data: ${JSON.stringify(event)}\n\n`;
  for (const c of clients) c.write(data);
};

function startTask(kind: Task["kind"], jobId: string | null, fn: (r: Reporter) => Promise<string | void>) {
  task = { kind, jobId, stage: null, logs: [], progress: null, error: null, done: false, startedAt: Date.now() };
  const t = task;
  const r: Reporter = {
    log: (m) => {
      const line = `${new Date().toLocaleTimeString("id-ID")}  ${m}`;
      t.logs.push(line);
      if (t.logs.length > 300) t.logs.shift();
      console.log(m);
      send({ type: "log", line });
    },
    stage: (s) => {
      t.stage = s;
      send({ type: "stage", stage: s, jobId: t.jobId });
    },
    progress: (stage, done, total) => {
      t.progress = { stage, done, total };
      send({ type: "progress", ...t.progress });
    },
  };
  fn(r)
    .then((id) => {
      if (typeof id === "string") t.jobId = id;
      t.done = true;
      send({ type: "done", kind, jobId: t.jobId });
    })
    .catch((e: Error) => {
      t.error = e.message;
      t.done = true;
      r.log(`ERROR: ${e.message}`);
      send({ type: "error", kind, jobId: t.jobId, message: e.message });
    });
}
const busy = () => Boolean(task && !task.done);

/* ---------------- Helper HTTP ---------------- */

const json = (res: http.ServerResponse, status: number, body: unknown) => {
  res.writeHead(status, { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" });
  res.end(JSON.stringify(body));
};

/**
 * Untuk langkah AI yang lama (30-120+ detik). Cloudflare memutus request yang diam lebih dari
 * 100 detik (error 524), jadi status 200 dikirim segera, lalu satu spasi tiap 15 detik sampai
 * JSON-nya siap. JSON.parse mengabaikan spasi di depan. Error dikirim sebagai { error } dengan
 * status 200, dan api() di halaman web menganggap `error` sebagai gagal.
 */
async function jsonKeepAlive(res: http.ServerResponse, fn: () => Promise<unknown>) {
  res.writeHead(200, { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store", "X-Accel-Buffering": "no" });
  res.write(" ");
  const ping = setInterval(() => res.write(" "), 15_000);
  let out: unknown;
  try {
    out = await fn();
  } catch (e) {
    console.error(e);
    out = { error: (e as Error).message };
  } finally {
    clearInterval(ping);
  }
  res.end(JSON.stringify(out));
}

const readBody = (req: http.IncomingMessage): Promise<Record<string, unknown>> =>
  new Promise((resolve, reject) => {
    let s = "";
    req.on("data", (d) => {
      s += d;
      if (s.length > 2_000_000) reject(new Error("Body terlalu besar"));
    });
    req.on("end", () => {
      try {
        resolve(s ? JSON.parse(s) : {});
      } catch {
        reject(new Error("JSON tidak valid"));
      }
    });
  });

const readRaw = (req: http.IncomingMessage, limit: number): Promise<Buffer> =>
  new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let n = 0;
    req.on("data", (d: Buffer) => {
      n += d.length;
      if (n > limit) {
        reject(new Error("Potongan terlalu besar"));
        req.destroy();
      } else chunks.push(d);
    });
    req.on("end", () => resolve(Buffer.concat(chunks)));
    req.on("error", reject);
  });

const safeId =(id: string) => (/^[\w.-]+$/.test(id) ? id : null);

const MIME: Record<string, string> = {
  ".mp4": "video/mp4",
  ".srt": "text/plain; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".html": "text/html; charset=utf-8",
};

/** Kirim file dengan dukungan Range (agar video bisa di-seek di browser). */
function sendFile(req: http.IncomingMessage, res: http.ServerResponse, file: string, download = false) {
  if (!fs.existsSync(file)) return json(res, 404, { error: "File tidak ditemukan" });
  const stat = fs.statSync(file);
  const type = MIME[path.extname(file)] ?? "application/octet-stream";
  const headers: http.OutgoingHttpHeaders = { "Content-Type": type, "Accept-Ranges": "bytes", "Cache-Control": "no-store" };
  if (download) headers["Content-Disposition"] = `attachment; filename="${path.basename(path.dirname(file))}-${path.basename(file)}"`;
  const range = req.headers.range?.match(/bytes=(\d*)-(\d*)/);
  if (range) {
    const start = range[1] ? Number(range[1]) : 0;
    const end = range[2] ? Math.min(Number(range[2]), stat.size - 1) : stat.size - 1;
    res.writeHead(206, { ...headers, "Content-Range": `bytes ${start}-${end}/${stat.size}`, "Content-Length": end - start + 1 });
    fs.createReadStream(file, { start, end }).pipe(res);
    return;
  }
  res.writeHead(200, { ...headers, "Content-Length": stat.size });
  fs.createReadStream(file).pipe(res);
}

/* ---------------- Hubungkan TikTok lewat browser ---------------- */

// state OAuth yang masih berlaku (10 menit), agar callback hanya menerima login yang dimulai dari Studio ini.
const tiktokStates = new Map<string, number>();

/** Harus sama persis dengan Redirect URI di app TikTok. */
function tiktokRedirectUri(req: http.IncomingMessage): string {
  return env("TIKTOK_REDIRECT_URI") || `https://${req.headers.host}/api/tiktok/callback`;
}

function backToStudio(res: http.ServerResponse, result: string) {
  res.writeHead(302, { Location: `/?tiktok=${encodeURIComponent(result)}` });
  res.end();
}

/* ---------------- Rute ---------------- */

async function handle(req: http.IncomingMessage, res: http.ServerResponse) {
  const url = new URL(req.url ?? "/", `http://${HOST}:${PORT}`);
  const p = url.pathname;
  const m = req.method ?? "GET";

  if (m === "GET" && (p === "/" || p === "/index.html")) return sendFile(req, res, path.join(WEB, "index.html"));

  if (m === "GET" && p === "/api/tiktok/login") {
    if (!tiktokKeys()) return backToStudio(res, "Isi TIKTOK_CLIENT_KEY dan TIKTOK_CLIENT_SECRET di .env server dulu, lalu restart Studio.");
    const now = Date.now();
    for (const [k, exp] of tiktokStates) if (exp < now) tiktokStates.delete(k);
    const state = crypto.randomBytes(16).toString("hex");
    tiktokStates.set(state, now + 10 * 60_000);
    res.writeHead(302, { Location: tiktokAuthorizeUrl(tiktokRedirectUri(req), state) });
    return res.end();
  }

  if (m === "GET" && p === "/api/tiktok/callback") {
    const state = url.searchParams.get("state") ?? "";
    const exp = tiktokStates.get(state);
    tiktokStates.delete(state);
    if (!exp || exp < Date.now()) return backToStudio(res, "Login TikTok kedaluwarsa atau tidak dimulai dari Studio. Coba klik Hubungkan TikTok lagi.");
    const code = url.searchParams.get("code");
    if (!code) return backToStudio(res, `TikTok tidak memberi izin: ${url.searchParams.get("error_description") || url.searchParams.get("error") || "dibatalkan"}`);
    try {
      await tiktokExchangeCode(code, tiktokRedirectUri(req));
      return backToStudio(res, "ok");
    } catch (e) {
      return backToStudio(res, `Gagal menghubungkan TikTok: ${(e as Error).message}`);
    }
  }

  if (m === "GET" && p === "/api/status") {
    return json(res, 200, {
      mode: USE_API ? "API key (bayar per pemakaian)" : "Langganan Claude (Claude Code)",
      platforms: PLATFORMS.map((x) => ({ name: x.name, ready: x.ready() })),
      tiktokKeys: tiktokKeys(),
      defaults: DEFAULT_CONFIG,
      gaya: GAYA_LABEL,
      suara: SUARA,
      render: workerStatus(),
      task,
    });
  }

  if (m === "GET" && p === "/api/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store", Connection: "keep-alive" });
    res.write(": ok\n\n");
    clients.add(res);
    const ping = setInterval(() => res.write(": ping\n\n"), 20000);
    req.on("close", () => {
      clearInterval(ping);
      clients.delete(res);
    });
    return;
  }

  if (m === "GET" && p === "/api/jobs") return json(res, 200, listJobs());

  let mm = p.match(/^\/api\/jobs\/([^/]+)$/);
  if (m === "GET" && mm) {
    const id = safeId(decodeURIComponent(mm[1]));
    const j = id ? loadJob(id) : null;
    return j ? json(res, 200, j) : json(res, 404, { error: "Job tidak ditemukan" });
  }

  mm = p.match(/^\/media\/([^/]+)\/(video\.mp4|subtitles\.srt|storyboard\.json)$/);
  if (m === "GET" && mm) {
    const id = safeId(decodeURIComponent(mm[1]));
    if (!id) return json(res, 400, { error: "id tidak valid" });
    return sendFile(req, res, path.join(jobDirOf(id), mm[2]), url.searchParams.has("download"));
  }

  if (m === "POST" && p === "/api/prompt") {
    const body = await readBody(req);
    const cfg = normalizeConfig(body as never);
    if (!cfg.topik) return json(res, 400, { error: "Gagasan masih kosong." });
    return jsonKeepAlive(res, async () => {
      const usage: UsageEntry[] = [];
      const prompt = await makeMasterPrompt(cfg, (u) => usage.push(u));
      return { prompt, config: cfg, usage };
    });
  }

  if (m === "POST" && p === "/api/plan") {
    const body = await readBody(req);
    const cfg = normalizeConfig((body.config ?? {}) as never);
    const prompt = String(body.prompt ?? "").trim();
    if (!prompt) return json(res, 400, { error: "Prompt masih kosong." });
    return jsonKeepAlive(res, async () => {
      const usage: UsageEntry[] = [];
      const plan = await makePlan(prompt, cfg, (u) => usage.push(u));
      return { plan, usage };
    });
  }

  if (m === "POST" && p === "/api/produce") {
    if (busy()) return json(res, 409, { error: "Masih ada proses berjalan. Tunggu sampai selesai." });
    const body = await readBody(req);
    const parsed = PlanSchema.safeParse(body.plan);
    if (!parsed.success || !parsed.data.scenes.length) return json(res, 400, { error: "Storyboard tidak valid." });
    const plan = parsed.data;
    plan.scenes = plan.scenes.filter((s) => s.narasi.trim());
    const cfg = normalizeConfig((body.config ?? {}) as never);
    const prompt = String(body.prompt ?? cfg.topik);
    const preUsage = cleanEntries(body.usage);
    startTask("produksi", null, (r) => produce(plan, prompt, cfg, r, preUsage));
    return json(res, 202, { ok: true });
  }

  if (m === "POST" && p === "/api/revise") {
    if (busy()) return json(res, 409, { error: "Masih ada proses berjalan." });
    const body = await readBody(req);
    const id = safeId(String(body.jobId ?? ""));
    const feedback = String(body.feedback ?? "").trim();
    if (!id || !feedback) return json(res, 400, { error: "Isi masukan revisi." });
    startTask("revisi", id, (r) => revise(id, feedback, r));
    return json(res, 202, { ok: true });
  }

  if (m === "POST" && p === "/api/upload") {
    if (busy()) return json(res, 409, { error: "Masih ada proses berjalan." });
    const body = await readBody(req);
    const id = safeId(String(body.jobId ?? ""));
    const names = Array.isArray(body.platforms) ? body.platforms.map(String) : [];
    if (!id || !names.length) return json(res, 400, { error: "Pilih minimal satu platform." });
    startTask("upload", id, async (r) => {
      await uploadJob(id, names, r);
    });
    return json(res, 202, { ok: true });
  }

  if (m === "POST" && p === "/api/rerender") {
    if (busy()) return json(res, 409, { error: "Masih ada proses berjalan." });
    const body = await readBody(req);
    const id = safeId(String(body.jobId ?? ""));
    if (!id) return json(res, 400, { error: "id tidak valid" });
    startTask("produksi", id, (r) => rerender(id, r));
    return json(res, 202, { ok: true });
  }

  /* ----- API worker (laptop render). Wajib header Authorization: Bearer <WORKER_TOKEN> ----- */
  if (p.startsWith("/api/worker/")) {
    if (!checkWorkerAuth(req.headers.authorization)) return json(res, 401, { error: "Token worker salah atau WORKER_TOKEN belum diisi" });
    const worker = String(req.headers["x-worker-name"] ?? "laptop").slice(0, 60);

    if (m === "POST" && p === "/api/worker/next") {
      const item = claimNext(worker);
      if (!item) {
        res.writeHead(204);
        return res.end();
      }
      return json(res, 200, item);
    }

    if (m === "GET" && p === "/api/worker/file") {
      const rel = String(url.searchParams.get("path") ?? "");
      if (!isNeededFile(rel)) return json(res, 403, { error: "File tidak dibutuhkan oleh render aktif" });
      const file = path.resolve(ROOT, "public", rel);
      if (!file.startsWith(path.resolve(ROOT, "public") + path.sep)) return json(res, 400, { error: "path tidak valid" });
      return sendFile(req, res, file);
    }

    mm = p.match(/^\/api\/worker\/(progress|upload|complete|fail)\/([\w-]+)$/);
    if (m === "POST" && mm) {
      const [, action, renderId] = mm;
      if (action === "upload") {
        const offset = Number(req.headers["x-offset"] ?? -1);
        const data = await readRaw(req, 16 * 1024 * 1024);
        const r = appendChunk(renderId, worker, offset, data);
        if (!r) return json(res, 404, { error: "Render tidak dikenal" });
        return json(res, r.ok ? 200 : 409, r);
      }
      const body = await readBody(req);
      if (action === "progress") return json(res, reportProgress(renderId, worker, body as never) ? 200 : 404, {});
      if (action === "complete") {
        const err = complete(renderId, worker, Number(body.size));
        return err ? json(res, 409, { error: err }) : json(res, 200, { ok: true });
      }
      return json(res, fail(renderId, worker, String(body.error ?? "")) ? 200 : 404, {});
    }
    return json(res, 404, { error: "Tidak ditemukan" });
  }

  mm = p.match(/^\/api\/open\/([^/]+)$/);
  if (m === "POST" && mm) {
    const id = safeId(decodeURIComponent(mm[1]));
    if (!id) return json(res, 400, { error: "id tidak valid" });
    if (process.platform !== "win32" || HOST !== "127.0.0.1") return json(res, 400, { error: `Folder ada di server: ${jobDirOf(id)}` });
    spawn("explorer", [jobDirOf(id)], { detached: true, stdio: "ignore" }).unref();
    return json(res, 200, { ok: true });
  }

  json(res, 404, { error: "Tidak ditemukan" });
}

const server = http.createServer((req, res) => {
  handle(req, res).catch((e: Error) => {
    console.error(e);
    if (!res.headersSent) json(res, 500, { error: e.message });
  });
});
server.requestTimeout = 0; // langkah prompt/storyboard bisa memakan beberapa menit

server.listen(PORT, HOST, () => {
  const url = `http://localhost:${PORT}`;
  console.log(`Studio Video berjalan di ${url}\nBiarkan jendela ini terbuka selama memakai Studio. Tutup jendela untuk berhenti.`);
  console.log(`Render: ${workerStatus().mode === "remote" ? "lewat laptop render (worker)" : "di komputer ini"}`);
  if (process.platform === "win32" && !process.argv.includes("--no-open")) spawn("cmd", ["/c", "start", '""', url], { shell: true, detached: true });
});
