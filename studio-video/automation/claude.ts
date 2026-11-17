import Anthropic from "@anthropic-ai/sdk";
import { zodOutputFormat } from "@anthropic-ai/sdk/helpers/zod";
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { z } from "zod";
import type { SceneWord } from "../src/auto/types";
import { DEFAULT_CONFIG, GAYA_LABEL, maxWords, sceneRange, size, SUARA, type VideoConfig } from "./config";
import { env, ROOT } from "./env";

// Dua cara memanggil Claude:
// - ANTHROPIC_API_KEY diisi  -> Claude API (bayar per pemakaian)
// - ANTHROPIC_API_KEY kosong -> Claude Code CLI (`claude -p`), memakai langganan Claude Anda
export const USE_API = Boolean(env("ANTHROPIC_API_KEY"));
const MODEL = env("CLAUDE_MODEL", "claude-opus-5-5");
let _client: Anthropic | null = null;
const api = () => (_client ??= new Anthropic());

type CliResult = { is_error?: boolean; subtype?: string; result?: string; structured_output?: unknown; session_id?: string };

/** Jalankan Claude Code tanpa tampilan interaktif dan tanpa tools (hanya menjawab teks). */
function runClaudeCli(args: string[], prompt: string): Promise<CliResult> {
  return new Promise((resolve, reject) => {
    const p = spawn(
      "claude",
      ["-p", "--output-format", "json", "--tools", "", "--model", env("CLAUDE_CLI_MODEL", "opus"), ...args],
      { cwd: ROOT, stdio: ["pipe", "pipe", "pipe"] },
    );
    let out = "";
    let err = "";
    p.stdout.on("data", (d) => (out += d));
    p.stderr.on("data", (d) => (err += d));
    p.on("error", (e) => reject(new Error(`Tidak bisa menjalankan 'claude' (Claude Code terpasang?): ${e.message}`)));
    p.on("close", (code) => {
      let res: CliResult;
      try {
        res = JSON.parse(out);
      } catch {
        return reject(new Error(`Claude Code gagal (exit ${code}): ${(err || out).slice(0, 1000)}`));
      }
      if (res.is_error) {
        return reject(
          new Error(`Claude Code error (${res.subtype}): ${String(res.result ?? "").slice(0, 1000)}\n` +
            "Jika karena batas pemakaian langganan, tunggu sampai kuota pulih atau isi ANTHROPIC_API_KEY."),
        );
      }
      resolve(res);
    });
    p.stdin.end(prompt);
  });
}

// ---------- 0. Gagasan -> Master Prompt ----------

const MASTER_SYSTEM = `Kamu adalah prompt engineer dan produser video animasi. Ubah gagasan mentah pengguna menjadi MASTER PROMPT yang jelas, lengkap, dan siap dipakai AI lain untuk menulis storyboard video animasi berbahasa Indonesia.

Tulis dalam bahasa Indonesia, format teks biasa dengan judul bagian bernomor. Jangan pakai tabel. Jangan menulis kode. Jangan menulis naskah lengkap; tulis arahan.
Bagian wajib:
1. KONFIGURASI: judul kerja, topik, durasi, rasio, gaya animasi, target penonton, gaya narasi, suara (salin dari konfigurasi).
2. TUJUAN & PESAN UTAMA: apa yang harus dipahami/dirasakan penonton dalam 1-2 kalimat.
3. HOOK: 2-3 opsi kalimat pembuka yang memancing rasa penasaran dalam 2 detik pertama.
4. ALUR CERITA: opening, poin-poin inti berurutan (fakta/angka konkret yang akurat), klimaks/insight, penutup + ajakan.
5. ARAHAN VISUAL: palet, motif visual, elemen yang wajib muncul (diagram, angka, perbandingan, karakter jika gaya kartun), ritme potongan shot.
6. ARAHAN AUDIO: karakter suara narator, tempo, momen efek suara.
7. BATASAN: durasi, jumlah kata narasi maksimal, hal yang harus dihindari (misinformasi, istilah sulit tanpa penjelasan, dll.).
Jika gagasan ambigu, pilih tafsiran paling menarik dan masuk akal; jangan bertanya balik.`;

export async function makeMasterPrompt(cfg: VideoConfig): Promise<string> {
  const { width, height } = size(cfg.rasio);
  const input = `GAGASAN:\n${cfg.topik}\n\nKONFIGURASI:\n- Judul: ${cfg.judul ?? "(buatkan)"}\n- Durasi target: ${cfg.durasi} detik (maks ${maxWords(cfg.durasi)} kata narasi)\n- Rasio: ${cfg.rasio} (${width}x${height}, 30 fps, MP4 H.264 + AAC)\n- Gaya animasi: ${GAYA_LABEL[cfg.gaya]}\n- Target penonton: ${cfg.audiens}\n- Gaya narasi: ${cfg.gayaNarasi}\n- Suara: ${SUARA.find((s) => s.id === cfg.suara)?.label ?? cfg.suara}\n${cfg.catatan ? `- Catatan tambahan: ${cfg.catatan}\n` : ""}`;
  if (!USE_API) {
    const res = await runClaudeCli(["--effort", "low", "--no-session-persistence", "--system-prompt", MASTER_SYSTEM], input);
    return String(res.result ?? "").trim();
  }
  const res = await api().messages.create({
    model: MODEL,
    max_tokens: 8000,
    output_config: { effort: "low" },
    system: MASTER_SYSTEM,
    messages: [{ role: "user", content: input }],
  });
  if (res.stop_reason === "refusal") throw new Error("Claude menolak permintaan ini.");
  return res.content
    .filter((b): b is Anthropic.TextBlock => b.type === "text")
    .map((b) => b.text)
    .join("\n")
    .trim();
}

// ---------- 1. Rencana video (naskah + metadata upload) ----------

export const PlanSchema = z.object({
  judul: z.string().describe("Judul video, maks 90 karakter, menarik, bahasa Indonesia"),
  deskripsi: z.string().describe("Deskripsi YouTube 2-4 kalimat + hashtag"),
  caption: z.string().describe("Caption untuk TikTok/Instagram/Facebook, maks 300 karakter, termasuk 3-6 hashtag"),
  tags: z.array(z.string()).describe("5-10 tag YouTube"),
  scenes: z
    .array(
      z.object({
        narasi: z.string().describe("Kalimat narasi yang dibacakan (1-2 kalimat pendek, bahasa Indonesia lisan)"),
        visual: z
          .string()
          .describe(
            "Rencana 2-4 shot cepat untuk scene ini. Tiap shot: tema (dark/cream/accent/night), TEKS LAYAR BESAR (1-4 kata kunci), elemen visual (kartu, angka berjalan, ikon, diagram), dan kata narasi yang memicu potongan shot",
          ),
      }),
    )
    .describe("Daftar scene sesuai jumlah & batas kata di instruksi sistem"),
});
export type Plan = z.infer<typeof PlanSchema>;

const planSystem = (cfg: VideoConfig) => {
  const sc = sceneRange(cfg.durasi);
  const orient = cfg.rasio === "16:9" ? "horizontal 16:9 (YouTube biasa)" : "vertikal 9:16 (Shorts, TikTok, Reels)";
  return `Kamu adalah penulis naskah dan sutradara video animasi berbahasa Indonesia, bergaya motion design yang cepat dan modern.
Dari master prompt / permintaan pengguna, buat rencana video ${orient} berdurasi sekitar ${cfg.durasi} detik: ${sc.min}-${sc.max} scene, total narasi semua scene MAKSIMAL ${maxWords(cfg.durasi)} kata. Gaya narasi: ${cfg.gayaNarasi}. Target penonton: ${cfg.audiens}.${cfg.judul ? ` Gunakan judul: "${cfg.judul}".` : ""}`;
};

const PLAN_RULES = `

NASKAH
- Scene pertama adalah hook kuat dalam 2 detik pertama: klaim mengejutkan, angka, atau pertanyaan yang bikin penasaran. Jangan membuka dengan "Halo" atau "Pernahkah kamu".
- Kalimat pendek, gaya lisan anak muda yang tetap sopan. Satu ide per scene. Akurat secara ilmiah/faktual.
- Gunakan angka konkret jika relevan (angka bagus untuk animasi counter).
- Scene terakhir merangkum dalam 1 kalimat dan mengajak follow/komentar.
- Narasi untuk text-to-speech: tanpa singkatan, simbol, atau emoji; tulis angka dengan digit (contoh: 2749, 90 persen).

VISUAL (gaya kinetic explainer)
- Setiap scene dipecah menjadi 2-4 shot berdurasi 1,5-3 detik. Tidak boleh ada visual yang sama lebih dari 3 detik.
- Tiap shot punya TEKS LAYAR BESAR berisi 1-4 kata kunci (bukan kalimat panjang), dengan 1 kata berwarna aksen.
- Tema latar berselang-seling antar shot: dark (hitam elegan), cream (krem terang), accent (oranye penuh, untuk momen penting), night (biru gelap).
- Pakai elemen: kartu UI, angka yang berjalan (counter), progress bar, ikon dalam kotak berwarna, kartu statistik berjajar, jendela aplikasi, chip/label, diagram SVG sederhana dengan panah yang tergambar, perbandingan kiri-kanan, centang/silang.
- Sebutkan kata narasi yang memicu tiap pergantian shot (contoh: "potong di kata 'tapi'").
- Jangan meminta foto, video asli, atau gambar orang realistis.`;

export async function makePlan(prompt: string, cfg: VideoConfig = DEFAULT_CONFIG): Promise<Plan> {
  const kartun =
    cfg.gaya === "kartun"
      ? `\n\nGAYA KARTUN 2D\n- Boleh ada karakter/maskot kartun flat sederhana (bentuk SVG: kepala bulat, mata titik, badan sederhana) dan benda yang bergerak (berjalan, melambai, berkedip, memantul).\n- Deskripsikan aksi karakter per shot dengan jelas, tetap disertai teks layar besar.`
      : "";
  const PLAN_SYSTEM = planSystem(cfg) + "\n" + PLAN_RULES + kartun;
  if (!USE_API) {
    const res = await runClaudeCli(
      [
        "--effort", "medium",
        "--no-session-persistence",
        "--system-prompt", PLAN_SYSTEM,
        "--json-schema", JSON.stringify({ ...z.toJSONSchema(PlanSchema), $schema: undefined }),
      ],
      prompt,
    );
    return PlanSchema.parse(res.structured_output ?? JSON.parse(res.result ?? "{}"));
  }
  const res = await api().messages.parse({
    model: MODEL,
    max_tokens: 16000,
    output_config: { effort: "medium", format: zodOutputFormat(PlanSchema) },
    system: PLAN_SYSTEM,
    messages: [{ role: "user", content: prompt }],
  });
  if (res.stop_reason === "refusal") throw new Error("Claude menolak permintaan ini.");
  if (!res.parsed_output) throw new Error("Gagal membaca rencana video dari Claude.");
  return res.parsed_output;
}

// ---------- 2. Penulis kode animasi Remotion ----------

const FENCE = "```";

/** Prompt sistem penulis kode, disesuaikan dengan rasio & gaya video. */
export function codeSystem(cfg: VideoConfig = DEFAULT_CONFIG): string {
  const { width: w, height: h } = size(cfg.rasio);
  const canvas =
    cfg.rasio === "16:9"
      ? `CANVAS: ${w}x${h} (landscape), 30 fps. Keep content between y=70 and y=${h - 230}; the bottom ${230}px are reserved for subtitles. Use the width: side-by-side layouts (text left, visual right; 3-4 cards in a row) work well. <Safe> already adapts to the canvas. Use svg viewBox="0 0 ${w} ${h}" for full-frame drawings.`
      : `CANVAS: ${w}x${h} (vertical), 30 fps. Keep content between y=180 and y=${h - 440}; the bottom area is reserved for subtitles. Use svg viewBox="0 0 ${w} ${h}" for full-frame drawings.`;
  const style =
    cfg.gaya === "kartun"
      ? `\n\nCARTOON STYLE (requested)\n- Besides kinetic text, build flat 2D cartoon characters and objects in inline SVG as reusable React components (e.g. const Mascot: React.FC<{ mood: "happy" | "think" }>): simple round shapes, dot eyes, consistent colors across scenes.\n- Animate them: blinking (eye scaleY), bouncing walk cycles (sin-based), waving arms (rotate around shoulder), head tilts, squash & stretch on landing, objects sliding/rotating. Characters must stay on-model in every scene.\n- Friendly faces ARE allowed in this style.`
      : "";
  return CODE_SYSTEM_BASE.replace("__CANVAS__", canvas) + style;
}

const CODE_SYSTEM_BASE = `You are a top-tier motion designer writing Remotion (v4, React 18, TypeScript) code for a fast, punchy explainer video in the style of premium "kinetic typography + UI cards" reels: quick cuts every 1.5-3 s, huge bold words popping in one by one, alternating dark / cream / orange backgrounds with a subtle grid, animated counters, progress bars, app windows, stat cards, icons in colored rounded squares, and constant purposeful motion.

OUTPUT: exactly one ${FENCE}tsx code block containing the complete file src/generated/Scenes.tsx. No other files.

FILE CONTRACT
- Imports allowed ONLY from "react", "remotion", "../auto/kit", plus: import type { SceneComponent } from "../auto/types";
- One component per scene: const Scene1: SceneComponent = ({ durationInFrames, words }) => { ... }
- End with: export const SCENES: SceneComponent[] = [Scene1, Scene2, ...]; exactly the given number of scenes, in order.
- Each scene is mounted in its own <Sequence>: useCurrentFrame() starts at 0 per scene. \`words\` = narration words with frame timings {t, s, e} relative to the scene start.
- The wrapper already adds narration audio, karaoke subtitles, a top progress bar, scene transitions and a whoosh sound. Do NOT add narration <Audio>, subtitles or a narration text box.

__CANVAS__

THE KIT (import from "../auto/kit"). Use it heavily; it makes everything look consistent and polished.
- <Shots durationInFrames={durationInFrames} cuts={[f2, f3]} shots={[{ theme: "dark", content: <...> }, { theme: "cream", content: <...> }, ...]} />
  Splits the scene into fast shots with punch-zoom + whoosh transitions. cuts = start frames of shot 2..n (length shots.length-1); compute them with wordAt(words, "kata", fallbackFrame) so cuts land on narration words. Themes: "dark" | "cream" | "accent" | "night". EVERY scene must use <Shots> with 2-4 shots; each shot 45-90 frames.
  Inside a shot, useCurrentFrame() and all kit \`delay\` props count from the START OF THAT SHOT. To time something to a word inside shot k, use wordAt(...) - cutStartOfShotK.
- <Safe gap={40}>...</Safe> vertical centered stack in the safe area. <At x y>...</At> absolute placement by center point.
- <Kinetic text={"BUANG WAKTU\\nLEBIH DARI *DUA JAM*"} size={120} delay={0} stagger={4} uppercase? align? /> word-by-word pop-in; *asterisks* = accent color, "\\n" = line break. Use size 100-150 for headline words. This is the main visual device: big, bold, few words.
- <Counter to={2749} from={0} delay duration={40} size={220} suffix=" jam" mono? /> animated number in id-ID format.
- <Card delay x? y? w? h? pad? tilt? float? from?>...</Card>, <Window title="claude.ai · chat" w={920} delay>...</Window>, <Chip text icon? tone="soft|solid|accent" delay />, <Bar value={0.8} label showPercent delay duration width />, <StatCard icon="laptop" iconBg="#76b900" value="GTX 1650" label="kartu grafis" delay />, <IconBadge name="bolt" size={140} bg="#ff5a36" delay? />, <Icon name size color />, <Marker text delay size /> (highlighter swipe), <Pop delay from="up|down|left|right|scale">...</Pop>, <Float>...</Float>, <Sfx name="pop|tick|whoosh" at={frame} volume={0.4} /> for accent sounds (use sparingly: pops on key reveals).
- Inside an <svg>, <Draw d="M..." delay duration stroke width /> draws a path progressively (arrows, connectors, diagrams).
- useTheme() returns the current shot palette {bg, fg, muted, card, accent, accent2, ...}; useEnter(delay) spring 0..1; useProgress(delay, duration) eased 0..1; useSize() returns {w, h} of the canvas; FONT, MONO, CLAMP.
- Icon names: check, x, clock, bolt, fire, snow, arrowRight, arrowUp, arrowDown, lock, bulb, laptop, phone, heart, star, warning, question, play, eye, drop, sun, chart, users, money, plus, minus, search, gear, globe, rocket, sparkle.

ART DIRECTION
- Each shot = ONE focal idea: a giant kinetic headline, OR a hero number, OR a card/diagram composition with a short headline. Big type, generous whitespace, high contrast.
- Vary layouts between shots: centered headline; number + label + bar; row of 3 StatCards; app Window with chat bubbles; left-vs-right comparison cards with check/x badges; SVG diagram with Draw arrows; IconBadge + headline.
- The first shot of Scene1 is the hook: a huge kinetic headline (theme "accent" or "dark") visible from frame 0-3.
- Custom SVG illustrations are welcome (objects, diagrams) but keep them flat, clean and on-palette (theme accent #ff5a36, accent2, fg, muted).
- Everything keeps moving subtly (Float, drift, staggered pops); nothing appears without an entrance.

CODE RULES
- Deterministic only: no Math.random, Date, timers, useEffect, useState, fetch, external assets/URLs/fonts/images. Use random("seed") from "remotion" for pseudo-randomness.
- Components that call hooks (useCurrentFrame, useTheme, ...) must be real React components (const Foo: React.FC = () => ...), not plain functions called inline, and must be rendered inside the shot content so their frame is shot-relative.
- interpolate input ranges must be strictly increasing; pass CLAMP when values must not overshoot.
- Must type-check under TypeScript strict mode and render without runtime errors.`;

// API: riwayat pesan disimpan di sini. CLI: percakapan disimpan Claude Code, dilanjutkan via session id.
// `system` disimpan agar revisi memakai prompt sistem yang sama dengan saat kode pertama ditulis.
export type CodeSession = { messages: Anthropic.Beta.BetaMessageParam[]; cliSessionId?: string; system?: string };

function extractCode(content: Anthropic.Beta.BetaContentBlock[] | string): string {
  const text =
    typeof content === "string"
      ? content
      : content
          .filter((b): b is Anthropic.Beta.BetaTextBlock => b.type === "text")
          .map((b) => b.text)
          .join("\n");
  const re = new RegExp(FENCE + "(?:tsx|typescript|ts|jsx)?\\s*\\n([\\s\\S]*?)" + FENCE, "g");
  const blocks = [...text.matchAll(re)].map((m) => m[1]);
  if (!blocks.length) throw new Error("Claude tidak mengembalikan blok kode.");
  return blocks.sort((a, b) => b.length - a.length)[0].trim() + "\n";
}

async function ask(session: CodeSession, userText: string): Promise<string> {
  const CODE_SYSTEM = session.system ?? codeSystem();
  if (!USE_API) {
    const first = !session.cliSessionId;
    session.cliSessionId ??= randomUUID();
    const res = await runClaudeCli(
      [
        "--effort", "high",
        "--system-prompt", CODE_SYSTEM,
        ...(first ? ["--session-id", session.cliSessionId] : ["--resume", session.cliSessionId]),
      ],
      userText,
    );
    return extractCode(res.result ?? "");
  }
  session.messages.push({ role: "user", content: userText });
  const stream = api().beta.messages.stream({
    model: MODEL,
    max_tokens: 64000,
    betas: ["server-side-fallback-2026-07-01"],
    fallbacks: "default",
    output_config: { effort: "high" },
    system: CODE_SYSTEM,
    messages: session.messages,
  });
  const msg = await stream.finalMessage();
  // Simpan seluruh content (termasuk blok thinking) agar percakapan tetap utuh untuk revisi.
  session.messages.push({ role: "assistant", content: msg.content });
  if (msg.stop_reason === "refusal") throw new Error("Claude menolak menulis kode untuk video ini.");
  if (msg.stop_reason === "max_tokens") throw new Error("Kode terpotong (max_tokens).");
  return extractCode(msg.content);
}

export async function writeSceneCode(
  plan: Plan,
  durations: number[],
  words: SceneWord[][] = [],
  cfg: VideoConfig = DEFAULT_CONFIG,
): Promise<{ code: string; session: CodeSession }> {
  const session: CodeSession = { messages: [], system: codeSystem(cfg) };
  const brief = plan.scenes
    .map((s, i) => {
      const timing = words[i]?.length ? `\n  Word start frames: ${words[i].map((w) => `${w.t}@${w.s}`).join(" ")}` : "";
      return `Scene${i + 1} - durationInFrames: ${durations[i]} (${(durations[i] / 30).toFixed(1)} s)\n  Narration: ${s.narasi}${timing}\n  Visual: ${s.visual}`;
    })
    .join("\n\n");
  const code = await ask(
    session,
    `Video title: ${plan.judul}\nNumber of scenes: ${plan.scenes.length}\n\n${brief}\n\nWrite src/generated/Scenes.tsx.`,
  );
  return { code, session };
}

export async function fixSceneCode(session: CodeSession, error: string): Promise<string> {
  return ask(
    session,
    `The code failed. Fix the cause and return the complete corrected file.\n\nError output:\n${error.slice(-6000)}`,
  );
}

export async function reviseSceneCode(session: CodeSession, feedback: string): Promise<string> {
  return ask(
    session,
    `The user watched the rendered video and requests these changes (in Indonesian):\n"${feedback}"\n\nApply them and return the complete updated file. Keep the same number of scenes and the same durations.`,
  );
}
