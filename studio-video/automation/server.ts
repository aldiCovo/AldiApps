// Studio Video: aplikasi web lokal (http://localhost:4321) untuk alur
// Gagasan -> Master Prompt -> Storyboard -> Kode + Render -> Preview -> Revisi / Upload.
import { spawn } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { apiKeyReady, makeMasterPrompt, makePlan, MODE_LABEL, PlanSchema, type AiMode } from "./claude";
import { DEFAULT_CONFIG, GAYA_LABEL, normalizeConfig, SUARA } from "./config";
import { env, ROOT } from "./env";
import { jobDirOf, jobOwner, listJobs, loadJob, monthlyUsage, PLATFORMS, produce, rerender, revise, uploadJob, type Reporter, type Stage } from "./pipeline";
import { appendLedger, cleanEntries, PRE_LANGKAH, thisMonth, type UsageEntry } from "./usage";
import { clearCookie, sameOrigin, sessionCookie, sessionUser } from "./session";
import { tiktokAuthorizeUrl, tiktokExchangeCode, tiktokKeys, tiktokProfile } from "./upload/tiktok";
import { checkPost, creatorInfo, directPost, NeedsLogin, parsePostOptions, userAccessToken, waitPublish, type CreatorInfo } from "./upload/tiktok-post";
import { createInvite, DATA_DIR, getUser, inviteUsable, listInvites, listUsers, newUser, normalizeCode, owner, publicUser, revokeInvite, saveUser, takeInvite, updateUser, type User } from "./users";
import { viaAccess } from "./access";
import { appendChunk, checkWorkerAuth, claimNext, complete, fail, isNeededFile, reportProgress, workerStatus } from "./queue";

const PORT = Number(process.env.STUDIO_PORT ?? 4321);
// Default hanya bisa diakses dari komputer ini. Untuk VPN (mis. Tailscale), isi STUDIO_HOST dengan IP VPN server.
const HOST = process.env.STUDIO_HOST?.trim() || "127.0.0.1";
const WEB = path.join(ROOT, "automation", "web");

/* ---------------- Tugas per pengguna, dijalankan satu per satu (render memakai src/generated bersama) ---------------- */

type Task = {
  kind: "produksi" | "revisi" | "upload";
  owner: string; // openId
  jobId: string | null;
  stage: Stage | null;
  logs: string[];
  progress: { stage: string; done: number; total: number } | null;
  error: string | null;
  queued: boolean; // menunggu giliran
  done: boolean;
  startedAt: number;
};
type Pending = { task: Task; priority: boolean; run: () => void };

const tasks = new Map<string, Task>(); // openId -> tugas terakhir
const pending: Pending[] = [];
let running = false;
const clients = new Map<http.ServerResponse, string>(); // koneksi SSE -> openId

const send = (openId: string, event: object) => {
  const data = `data: ${JSON.stringify(event)}\n\n`;
  for (const [c, who] of clients) if (who === openId) c.write(data);
};

function runNext() {
  if (running) return;
  // Pekerjaan pemilik Studio didahulukan; sisanya urut kedatangan.
  const i = Math.max(0, pending.findIndex((x) => x.priority));
  const next = pending.splice(i, 1)[0];
  if (!next) return;
  running = true;
  next.task.queued = false;
  for (const p of pending) send(p.task.owner, { type: "queue", position: queuePosition(p.task.owner) });
  next.run();
}

const queuePosition = (openId: string) => {
  const i = pending.findIndex((x) => x.task.owner === openId);
  return i < 0 ? 0 : i + 1;
};

function startTask(user: User, kind: Task["kind"], jobId: string | null, fn: (r: Reporter, t: Task) => Promise<string | void>) {
  const t: Task = { kind, owner: user.openId, jobId, stage: null, logs: [], progress: null, error: null, queued: true, done: false, startedAt: Date.now() };
  tasks.set(user.openId, t);
  const r: Reporter = {
    log: (m) => {
      const line = `${new Date().toLocaleTimeString("id-ID")}  ${m}`;
      t.logs.push(line);
      if (t.logs.length > 300) t.logs.shift();
      console.log(`[${user.nama}] ${m}`);
      send(t.owner, { type: "log", line });
    },
    stage: (s) => {
      t.stage = s;
      send(t.owner, { type: "stage", stage: s, jobId: t.jobId });
    },
    progress: (stage, done, total) => {
      t.progress = { stage, done, total };
      send(t.owner, { type: "progress", ...t.progress });
    },
  };
  const run = () => {
    t.startedAt = Date.now();
    fn(r, t)
      .then((id) => {
        if (typeof id === "string") t.jobId = id;
        t.done = true;
        send(t.owner, { type: "done", kind, jobId: t.jobId });
      })
      .catch((e: Error) => {
        t.error = e.message;
        t.done = true;
        r.log(`ERROR: ${e.message}`);
        send(t.owner, { type: "error", kind, jobId: t.jobId, message: e.message });
      })
      .finally(() => {
        running = false;
        runNext();
      });
  };
  pending.push({ task: t, priority: user.role === "owner", run });
  const pos = queuePosition(user.openId);
  if (running && pos) r.log(`Menunggu giliran (antrean ke-${pos}). Halaman ini boleh ditutup.`);
  runNext();
}
const busy = (openId: string) => {
  const t = tasks.get(openId);
  return Boolean(t && !t.done);
};

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
  ".jpg": "image/jpeg",
};

/** Kirim file dengan dukungan Range (agar video bisa di-seek di browser). */
function sendFile(req: http.IncomingMessage, res: http.ServerResponse, file: string, download = false, cache = "no-store") {
  if (!fs.existsSync(file)) return json(res, 404, { error: "File tidak ditemukan" });
  const stat = fs.statSync(file);
  const type = MIME[path.extname(file)] ?? "application/octet-stream";
  const headers: http.OutgoingHttpHeaders = { "Content-Type": type, "Accept-Ranges": "bytes", "Cache-Control": cache };
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

/* ---------------- Login dengan TikTok ---------------- */

// state OAuth yang masih berlaku (10 menit), agar callback hanya menerima login yang dimulai dari Studio ini.
type LoginState = { exp: number; undangan: string | null; klaim: boolean };
const tiktokStates = new Map<string, LoginState>();

/** Harus sama persis dengan Redirect URI di app TikTok. */
function tiktokRedirectUri(req: http.IncomingMessage): string {
  return env("TIKTOK_REDIRECT_URI") || `https://${req.headers.host}/api/tiktok/callback`;
}

const redirect = (res: http.ServerResponse, to: string, cookie?: string) => {
  res.writeHead(302, { Location: to, "Cache-Control": "no-store", ...(cookie ? { "Set-Cookie": cookie } : {}) });
  res.end();
};
/** Kembali ke halaman depan dengan pesan (ditampilkan oleh landing.html / index.html). */
const backWith = (res: http.ServerResponse, msg: string) => redirect(res, `/?masuk=${encodeURIComponent(msg)}`);

function startLogin(req: http.IncomingMessage, res: http.ServerResponse, undangan: string | null, klaim: boolean) {
  if (!tiktokKeys()) return backWith(res, "Login TikTok belum disiapkan di server (TIKTOK_CLIENT_KEY dan TIKTOK_CLIENT_SECRET).");
  const now = Date.now();
  for (const [k, v] of tiktokStates) if (v.exp < now) tiktokStates.delete(k);
  const state = crypto.randomBytes(16).toString("hex");
  tiktokStates.set(state, { exp: now + 10 * 60_000, undangan, klaim });
  redirect(res, tiktokAuthorizeUrl(tiktokRedirectUri(req), state));
}

const KONTAK = "studio@aldiab.ai.id";

// Studio di laptop (hanya bisa dibuka dari komputer itu sendiri): tanpa login, selalu sebagai pemilik.
const LOCAL_ID = "lokal";
const LOCAL_USER: User = {
  openId: LOCAL_ID, nama: "Pemilik (laptop)", avatar: "", role: "owner", limitUsd: 0, tiktok: null,
  invite: null, createdAt: "", lastLoginAt: "", disabled: false,
};
const localOnly = () => HOST === "127.0.0.1" && env("STUDIO_LOGIN") !== "1";

/**
 * Pengguna yang login dan aktif, atau null. Tanpa sesi TikTok, login Cloudflare Access yang sah
 * dianggap pemilik (Access hanya meloloskan pemilik), sehingga pemilik tetap bisa masuk sebelum
 * login TikTok disiapkan, dan selama path itu masih dikunci Access.
 */
async function currentUser(req: http.IncomingMessage): Promise<User | null> {
  if (localOnly()) return LOCAL_USER;
  const id = sessionUser(req);
  const u = id ? getUser(id) : null;
  if (u) return u.disabled ? null : u;
  if (await viaAccess(req)) return owner() ?? LOCAL_USER;
  return null;
}

/* ---------------- URL video sementara untuk TikTok (PULL_FROM_URL) ---------------- */

// TikTok mengambil video dari https://<host>/pull/<token>/video.mp4. Token acak, berlaku sampai posting
// selesai atau paling lama 1 jam; boleh diminta berulang karena TikTok bisa mengunduh per bagian.
const pulls = new Map<string, { file: string; exp: number }>();
function pullUrl(req: http.IncomingMessage, file: string): { url: string; done: () => void } {
  const now = Date.now();
  for (const [k, v] of pulls) if (v.exp < now) pulls.delete(k);
  const token = crypto.randomBytes(24).toString("hex");
  pulls.set(token, { file, exp: now + 3600_000 });
  return { url: `https://${req.headers.host}/pull/${token}/video.mp4`, done: () => pulls.delete(token) };
}

/** Durasi video job dalam detik, dari job.json. */
function jobSeconds(id: string): number {
  try {
    const j = JSON.parse(fs.readFileSync(path.join(jobDirOf(id), "job.json"), "utf8"));
    return j.scenes.reduce((a: number, s: { durationInFrames: number }) => a + s.durationInFrames, 0) / j.fps;
  } catch {
    return 0;
  }
}

/* ---------------- Mode Claude, pemakaian, dan batas ---------------- */

// Video pemilik memakai langganan Claude-nya; video kreator memakai API key (dibayar per pemakaian).
const modeFor = (u: User): AiMode => (u.role === "owner" ? "langganan" : "api");

/** Pemakaian bulan ini. Pemilik: gabungan job tanpa owner.json (lama/laptop) dan job dengan openId-nya. */
function usageFor(u: User, month = thisMonth()) {
  if (u.role !== "owner") return monthlyUsage(u.openId, u.openId, month);
  const a = monthlyUsage(null, LOCAL_ID, month);
  if (u.openId === LOCAL_ID) return a;
  const b = monthlyUsage(u.openId, u.openId, month);
  return { ...b, usd: a.usd + b.usd, tokens: a.tokens + b.tokens, calls: a.calls + b.calls, videos: a.videos + b.videos, renderSeconds: a.renderSeconds + b.renderSeconds };
}

/* ---------------- Rute ---------------- */

async function handle(req: http.IncomingMessage, res: http.ServerResponse) {
  const url = new URL(req.url ?? "/", `http://${HOST}:${PORT}`);
  const p = url.pathname;
  const m = req.method ?? "GET";
  const me = await currentUser(req);

  /* ----- Publik ----- */
  if (m === "GET" && p === "/healthz") return json(res, 200, { ok: true }); // readinessProbe Kubernetes

  const pm = p.match(/^\/pull\/([a-f0-9]{48})\/video\.mp4$/);
  if ((m === "GET" || m === "HEAD") && pm) {
    const it = pulls.get(pm[1]);
    if (!it || it.exp < Date.now()) return json(res, 404, { error: "Tidak ditemukan" });
    return sendFile(req, res, it.file);
  }
  if (m === "GET" && (p === "/" || p === "/index.html")) return sendFile(req, res, path.join(WEB, me ? "index.html" : "landing.html"));
  if (m === "GET" && (p === "/terms" || p === "/privacy")) return sendFile(req, res, path.join(WEB, `${p.slice(1)}.html`));
  // Contoh video hasil Studio untuk halaman depan (file tetap di automation/web/showcase).
  const sm = p.match(/^\/showcase\/([a-z0-9-]+\.(?:mp4|jpg))$/);
  if ((m === "GET" || m === "HEAD") && sm) return sendFile(req, res, path.join(WEB, "showcase", sm[1]), false, "public, max-age=86400");

  // Link undangan: /masuk?undangan=KODE
  if (m === "GET" && p === "/masuk") {
    if (me) return redirect(res, "/");
    const code = normalizeCode(url.searchParams.get("undangan") ?? "");
    if (code && !inviteUsable(code)) return backWith(res, `Kode undangan tidak berlaku atau sudah dipakai. Hubungi ${KONTAK}.`);
    return startLogin(req, res, code || null, false);
  }
  // Login, atau hubungkan ulang TikTok (izin baru / token kedaluwarsa) bagi yang sudah masuk.
  if (m === "GET" && p === "/api/tiktok/login") {
    if (me && me.openId === LOCAL_ID) return redirect(res, localOnly() ? "/" : "/admin/klaim");
    return startLogin(req, res, null, false);
  }

  if (m === "GET" && p === "/api/tiktok/callback") {
    const key = url.searchParams.get("state") ?? "";
    const st = tiktokStates.get(key);
    tiktokStates.delete(key);
    if (!st || st.exp < Date.now()) return backWith(res, "Login kedaluwarsa atau tidak dimulai dari Studio. Silakan coba lagi.");
    const code = url.searchParams.get("code");
    if (!code) return backWith(res, `TikTok tidak memberi izin: ${url.searchParams.get("error_description") || url.searchParams.get("error") || "dibatalkan"}`);
    try {
      const t = await tiktokExchangeCode(code, tiktokRedirectUri(req));
      const prof = await tiktokProfile(t.accessToken);
      const auth = { refreshToken: t.refreshToken, refreshExpiresAt: t.refreshExpiresAt, scope: t.scope };
      let u = getUser(t.openId);
      if (u) {
        if (u.disabled) return backWith(res, `Akun ini dinonaktifkan. Hubungi ${KONTAK}.`);
        u = saveUser({ ...u, ...prof, tiktok: auth, lastLoginAt: new Date().toISOString(), role: st.klaim && !owner() ? "owner" : u.role });
      } else if (st.klaim) {
        if (owner()) return backWith(res, "Pemilik Studio sudah ditetapkan.");
        u = newUser(t.openId, prof.nama, prof.avatar, "owner", null, auth);
      } else if (st.undangan && takeInvite(st.undangan, t.openId)) {
        u = newUser(t.openId, prof.nama, prof.avatar, "creator", st.undangan, auth);
      } else {
        return backWith(res, `Studio khusus kreator undangan. Minta link undangan ke ${KONTAK}.`);
      }
      return redirect(res, "/", sessionCookie(req, u.openId));
    } catch (e) {
      console.error(e);
      return backWith(res, `Login TikTok gagal: ${(e as Error).message}`);
    }
  }

  // Penetapan pemilik Studio, sekali saja. Path /admin dikunci Cloudflare Access.
  if (m === "GET" && p === "/admin/klaim") {
    if (!(await viaAccess(req))) return json(res, 403, { error: "Hanya lewat Cloudflare Access" });
    if (owner()) return redirect(res, "/");
    const sid = sessionUser(req);
    if (sid && getUser(sid)) {
      updateUser(sid, { role: "owner" });
      return redirect(res, "/");
    }
    return startLogin(req, res, null, true);
  }

  /* ----- Worker (laptop render): memakai token, bukan sesi ----- */
  if (p.startsWith("/api/worker/")) return handleWorker(req, res, url, p, m);

  /* ----- Selain di atas: wajib login ----- */
  if (!me) {
    if (p.startsWith("/api/") || p.startsWith("/media/")) return json(res, 401, { error: "Silakan login dulu." });
    return redirect(res, "/");
  }
  if (m === "POST" && !sameOrigin(req)) return json(res, 403, { error: "Permintaan ditolak (asal tidak cocok)." });

  if (m === "POST" && p === "/keluar") return redirect(res, "/", clearCookie(req));

  const isOwner = me.role === "owner";

  // Admin: dikunci Cloudflare Access di tepi, dan di sini dicek lagi (token Access sah + peran pemilik).
  if (p === "/admin" || p.startsWith("/admin/")) {
    if (!isOwner || !(await viaAccess(req))) return json(res, 403, { error: "Khusus pemilik Studio." });
    return handleAdmin(req, res, url, p, m);
  }
  const mine = (id: string) => {
    const o = jobOwner(id);
    return o === me.openId || (o === null && isOwner);
  };
  // Sampai Claude per pengguna dan batas biaya siap, hanya pemilik yang boleh memakai AI dan upload.
  /** true = sudah dijawab 403 (bukan pemilik), route harus berhenti. */
  const ownerOnly = () => {
    if (isOwner) return false;
    json(res, 403, { error: `Upload TikTok untuk kreator segera dibuka. Info: ${KONTAK}` });
    return true;
  };
  /** Kreator: API key harus ada dan pemakaian bulan ini di bawah batas. true = sudah dijawab, route berhenti. */
  const creatorGate = () => {
    if (isOwner) return false;
    if (!apiKeyReady()) {
      json(res, 503, { error: `Studio belum siap untuk kreator. Info: ${KONTAK}` });
      return true;
    }
    const used = usageFor(me).usd;
    if (used >= me.limitUsd) {
      json(res, 403, { error: `Batas pemakaian bulan ini sudah tercapai ($${used.toFixed(2)} dari $${me.limitUsd.toFixed(2)}). Hubungi ${KONTAK} untuk menaikkan batas.` });
      return true;
    }
    return false;
  };
  const mode = modeFor(me);
  /** Catat langkah prompt/storyboard di server (dasar tagihan), sekaligus kembalikan ke browser untuk tampilan. */
  const ledger = (usage: UsageEntry[]) => (u: UsageEntry) => {
    usage.push(u);
    appendLedger(DATA_DIR, me.openId, u);
  };

  if (m === "GET" && p === "/api/status") {
    const t = tasks.get(me.openId) ?? null;
    const render = workerStatus();
    return json(res, 200, {
      me: publicUser(me),
      mode: MODE_LABEL[modeFor(me)],
      usage: (({ usd, tokens, videos, renderSeconds }) => ({ month: thisMonth(), usd, tokens, videos, renderSeconds, limitUsd: isOwner ? null : me.limitUsd }))(usageFor(me)),
      // Di server TikTok punya kotak posting sendiri (akun masing-masing). Di laptop tetap lewat .env.
      platforms: PLATFORMS.filter((x) => (localOnly() ? true : isOwner && x.name !== "TikTok")).map((x) => ({ name: x.name, ready: x.ready() })),
      tiktokPost: !localOnly(),
      tiktokKeys: tiktokKeys(),
      defaults: DEFAULT_CONFIG,
      gaya: GAYA_LABEL,
      suara: SUARA,
      // Kreator tidak melihat job orang lain di antrean render.
      render: isOwner ? render : { ...render, queue: render.queue.map((q) => ({ ...q, jobId: mine(q.jobId) ? q.jobId : "" })) },
      task: t,
      queuePosition: queuePosition(me.openId),
    });
  }

  if (m === "GET" && p === "/api/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store", Connection: "keep-alive" });
    res.write(": ok\n\n");
    clients.set(res, me.openId);
    const ping = setInterval(() => res.write(": ping\n\n"), 20000);
    req.on("close", () => {
      clearInterval(ping);
      clients.delete(res);
    });
    return;
  }

  if (m === "GET" && p === "/api/jobs") return json(res, 200, listJobs((o) => o === me.openId || (o === null && isOwner)));

  let mm = p.match(/^\/api\/jobs\/([^/]+)$/);
  if (m === "GET" && mm) {
    const id = safeId(decodeURIComponent(mm[1]));
    const j = id && mine(id) ? loadJob(id) : null;
    return j ? json(res, 200, j) : json(res, 404, { error: "Job tidak ditemukan" });
  }

  mm = p.match(/^\/media\/([^/]+)\/(video\.mp4|subtitles\.srt|storyboard\.json)$/);
  if (m === "GET" && mm) {
    const id = safeId(decodeURIComponent(mm[1]));
    if (!id || !mine(id)) return json(res, 404, { error: "File tidak ditemukan" });
    return sendFile(req, res, path.join(jobDirOf(id), mm[2]), url.searchParams.has("download"));
  }

  if (m === "POST" && p === "/api/prompt") {
    if (creatorGate()) return;
    const body = await readBody(req);
    const cfg = normalizeConfig(body as never);
    if (!cfg.topik) return json(res, 400, { error: "Gagasan masih kosong." });
    return jsonKeepAlive(res, async () => {
      const usage: UsageEntry[] = [];
      const prompt = await makeMasterPrompt(cfg, ledger(usage), mode);
      return { prompt, config: cfg, usage };
    });
  }

  if (m === "POST" && p === "/api/plan") {
    if (creatorGate()) return;
    const body = await readBody(req);
    const cfg = normalizeConfig((body.config ?? {}) as never);
    const prompt = String(body.prompt ?? "").trim();
    if (!prompt) return json(res, 400, { error: "Prompt masih kosong." });
    return jsonKeepAlive(res, async () => {
      const usage: UsageEntry[] = [];
      const plan = await makePlan(prompt, cfg, ledger(usage), mode);
      return { plan, usage };
    });
  }

  if (m === "POST" && p === "/api/produce") {
    if (creatorGate()) return;
    if (busy(me.openId)) return json(res, 409, { error: "Masih ada proses berjalan. Tunggu sampai selesai." });
    const body = await readBody(req);
    const parsed = PlanSchema.safeParse(body.plan);
    if (!parsed.success || !parsed.data.scenes.length) return json(res, 400, { error: "Storyboard tidak valid." });
    const plan = parsed.data;
    plan.scenes = plan.scenes.filter((s) => s.narasi.trim());
    const cfg = normalizeConfig((body.config ?? {}) as never);
    const prompt = String(body.prompt ?? cfg.topik);
    const preUsage = cleanEntries(body.usage, PRE_LANGKAH); // hanya untuk tampilan biaya per video
    const ownerId = me.openId === LOCAL_ID ? null : me.openId;
    startTask(me, "produksi", null, (r, t) => produce(plan, prompt, cfg, r, preUsage, ownerId, (id) => (t.jobId = id), mode));
    return json(res, 202, { ok: true });
  }

  if (m === "POST" && p === "/api/revise") {
    if (creatorGate()) return;
    if (busy(me.openId)) return json(res, 409, { error: "Masih ada proses berjalan." });
    const body = await readBody(req);
    const id = safeId(String(body.jobId ?? ""));
    const feedback = String(body.feedback ?? "").trim();
    if (!id || !feedback) return json(res, 400, { error: "Isi masukan revisi." });
    if (!mine(id)) return json(res, 404, { error: "Job tidak ditemukan" });
    startTask(me, "revisi", id, (r) => revise(id, feedback, r));
    return json(res, 202, { ok: true });
  }

  if (m === "POST" && p === "/api/upload") {
    if (ownerOnly()) return;
    if (busy(me.openId)) return json(res, 409, { error: "Masih ada proses berjalan." });
    const body = await readBody(req);
    const id = safeId(String(body.jobId ?? ""));
    const names = Array.isArray(body.platforms) ? body.platforms.map(String) : [];
    if (!id || !names.length) return json(res, 400, { error: "Pilih minimal satu platform." });
    if (!mine(id)) return json(res, 404, { error: "Job tidak ditemukan" });
    startTask(me, "upload", id, async (r) => {
      await uploadJob(id, names, r);
    });
    return json(res, 202, { ok: true });
  }

  if (m === "GET" && p === "/api/tiktok/creator") {
    const id = safeId(url.searchParams.get("jobId") ?? "");
    if (!id || !mine(id)) return json(res, 404, { error: "Job tidak ditemukan" });
    try {
      const info = await creatorInfo(await userAccessToken(me.openId));
      return json(res, 200, { ...info, durationSec: jobSeconds(id) });
    } catch (e) {
      return json(res, e instanceof NeedsLogin ? 401 : 502, { error: (e as Error).message, needsLogin: e instanceof NeedsLogin });
    }
  }

  if (m === "POST" && p === "/api/tiktok/post") {
    if (busy(me.openId)) return json(res, 409, { error: "Masih ada proses berjalan." });
    const body = await readBody(req);
    const id = safeId(String(body.jobId ?? ""));
    if (!id || !mine(id)) return json(res, 404, { error: "Job tidak ditemukan" });
    const file = path.join(jobDirOf(id), "video.mp4");
    if (!fs.existsSync(file)) return json(res, 400, { error: "Video belum ada." });
    const opts = parsePostOptions(body);
    let token: string;
    let info: CreatorInfo;
    try {
      token = await userAccessToken(me.openId);
      info = await creatorInfo(token); // dibaca ulang, karena izin akun bisa berubah sejak halaman dibuka
    } catch (e) {
      return json(res, e instanceof NeedsLogin ? 401 : 502, { error: (e as Error).message, needsLogin: e instanceof NeedsLogin });
    }
    const bad = checkPost(info, opts, jobSeconds(id));
    if (bad) return json(res, 400, { error: bad });
    const pull = pullUrl(req, file);
    startTask(me, "upload", id, async (r) => {
      const resultsPath = path.join(jobDirOf(id), "uploads.json");
      const save = (v: string) => {
        const all = fs.existsSync(resultsPath) ? JSON.parse(fs.readFileSync(resultsPath, "utf8")) : {};
        fs.writeFileSync(resultsPath, JSON.stringify({ ...all, TikTok: v }, null, 2));
      };
      try {
        r.log(`Posting ke TikTok @${info.username} (privasi: ${opts.privacy})...`);
        const publishId = await directPost(token, pull.url, info, opts);
        r.log("Video dikirim. TikTok perlu beberapa menit untuk memprosesnya sebelum tampil di profil.");
        const out = await waitPublish(token, publishId, r.log);
        save(out);
        r.log(out);
      } catch (e) {
        save(`GAGAL: ${(e as Error).message}`);
        throw e;
      } finally {
        pull.done();
      }
    });
    return json(res, 202, { ok: true });
  }

  if (m === "POST" && p === "/api/rerender") {
    if (creatorGate()) return;
    if (busy(me.openId)) return json(res, 409, { error: "Masih ada proses berjalan." });
    const body = await readBody(req);
    const id = safeId(String(body.jobId ?? ""));
    if (!id || !mine(id)) return json(res, 404, { error: "Job tidak ditemukan" });
    startTask(me, "produksi", id, (r) => rerender(id, r));
    return json(res, 202, { ok: true });
  }

  mm = p.match(/^\/api\/open\/([^/]+)$/);
  if (m === "POST" && mm) {
    const id = safeId(decodeURIComponent(mm[1]));
    if (!id || !mine(id)) return json(res, 404, { error: "Job tidak ditemukan" });
    if (process.platform !== "win32" || HOST !== "127.0.0.1") return json(res, 400, { error: `Folder ada di server: ${jobDirOf(id)}` });
    spawn("explorer", [jobDirOf(id)], { detached: true, stdio: "ignore" }).unref();
    return json(res, 200, { ok: true });
  }

  json(res, 404, { error: "Tidak ditemukan" });
}

/* ----- Admin: undangan, kreator, batas, dan rekap biaya ----- */

const csvCell = (v: unknown) => {
  let t = String(v ?? "");
  // Nama dari TikTok bisa diawali = + - @, yang dibaca Excel sebagai rumus.
  if (typeof v === "string" && /^[=+\-@\t\r]/.test(t)) t = "'" + t;
  return /[",\n]/.test(t) ? `"${t.replace(/"/g, '""')}"` : t;
};

async function handleAdmin(req: http.IncomingMessage, res: http.ServerResponse, url: URL, p: string, m: string) {
  const bulanParam = url.searchParams.get("bulan") ?? "";
  const bulan = /^\d{4}-\d{2}$/.test(bulanParam) ? bulanParam : thisMonth();

  if (m === "GET" && p === "/admin") return sendFile(req, res, path.join(WEB, "admin.html"));

  const rows = () =>
    listUsers().map((u) => {
      const x = usageFor(u, bulan);
      return {
        ...publicUser(u),
        disabled: u.disabled,
        invite: u.invite,
        createdAt: u.createdAt,
        lastLoginAt: u.lastLoginAt,
        tiktok: Boolean(u.tiktok),
        usage: { videos: x.videos, calls: x.calls, tokens: x.tokens, usd: x.usd, usdPartial: x.usdPartial, renderSeconds: x.renderSeconds },
      };
    });

  if (m === "GET" && p === "/admin/api/data") {
    const base = `https://${req.headers.host}/masuk?undangan=`;
    return json(res, 200, {
      bulan,
      apiKeyReady: apiKeyReady(),
      users: rows(),
      invites: listInvites().reverse().map((i) => ({ ...i, link: base + i.code })),
    });
  }

  if (m === "GET" && p === "/admin/api/rekap.csv") {
    const head = ["bulan", "nama", "open_id", "peran", "video", "panggilan_ai", "token", "usd_harga_api", "jam_render", "batas_usd"];
    const lines = rows().map((u) =>
      [bulan, u.nama, u.openId, u.role, u.usage.videos, u.usage.calls, u.usage.tokens, u.usage.usd.toFixed(4), (u.usage.renderSeconds / 3600).toFixed(2), u.role === "owner" ? "" : u.limitUsd]
        .map(csvCell)
        .join(","),
    );
    res.writeHead(200, {
      "Content-Type": "text/csv; charset=utf-8",
      "Content-Disposition": `attachment; filename="aldi-studio-rekap-${bulan}.csv"`,
      "Cache-Control": "no-store",
    });
    return res.end([head.join(","), ...lines].join("\n") + "\n");
  }

  if (m === "POST" && p === "/admin/api/invite") {
    const body = await readBody(req);
    const inv = createInvite(String(body.catatan ?? ""));
    return json(res, 200, { ...inv, link: `https://${req.headers.host}/masuk?undangan=${inv.code}` });
  }

  if (m === "POST" && p === "/admin/api/invite/revoke") {
    const body = await readBody(req);
    return revokeInvite(String(body.code ?? "")) ? json(res, 200, { ok: true }) : json(res, 400, { error: "Undangan tidak ada atau sudah dipakai." });
  }

  if (m === "POST" && p === "/admin/api/user") {
    const body = await readBody(req);
    const u = getUser(String(body.openId ?? ""));
    if (!u) return json(res, 404, { error: "Pengguna tidak ditemukan" });
    const patch: Partial<User> = {};
    if (body.limitUsd !== undefined) {
      const v = Number(body.limitUsd);
      if (!Number.isFinite(v) || v < 0 || v > 10000) return json(res, 400, { error: "Batas harus angka 0 sampai 10000." });
      patch.limitUsd = Math.round(v * 100) / 100;
    }
    if (body.disabled !== undefined) {
      if (u.role === "owner") return json(res, 400, { error: "Pemilik tidak bisa dinonaktifkan." });
      patch.disabled = Boolean(body.disabled);
    }
    return json(res, 200, publicUser(updateUser(u.openId, patch)!));
  }

  return json(res, 404, { error: "Tidak ditemukan" });
}

/* ----- API worker (laptop render). Wajib header Authorization: Bearer <WORKER_TOKEN> ----- */
async function handleWorker(req: http.IncomingMessage, res: http.ServerResponse, url: URL, p: string, m: string) {
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

  const mm = p.match(/^\/api\/worker\/(progress|upload|complete|fail)\/([\w-]+)$/);
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
