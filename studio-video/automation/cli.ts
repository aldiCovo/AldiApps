import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import readline from "node:readline/promises";
import { makePlan, USE_API, type Plan } from "./claude";
import { DEFAULT_CONFIG, normalizeConfig, type VideoConfig } from "./config";
import { jobDirOf, PLATFORMS, produce, revise, uploadJob, type Reporter } from "./pipeline";

const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
let inputClosed = false;
rl.on("close", () => (inputClosed = true));
/** Tanya pengguna; mengembalikan null jika input terminal sudah tertutup. */
const question = async (q: string): Promise<string | null> => {
  if (inputClosed) return null;
  try {
    return (await rl.question(q)).trim();
  } catch {
    return null;
  }
};
const log = (s: string) => console.log(s);

const reporter: Reporter = {
  log: (m) => log(`   ${m}`),
  progress: (stage, done, total) => process.stdout.write(`\r   ${stage === "render" ? "Rendered" : "Encoded"} ${done}/${total}      `),
};

const openFile = (file: string) => spawn("cmd", ["/c", "start", '""', `"${file}"`], { shell: true, detached: true });

function printPlan(plan: Plan) {
  log(`\n   Judul   : ${plan.judul}`);
  log(`   Caption : ${plan.caption}`);
  plan.scenes.forEach((s, i) => log(`   ${i + 1}. ${s.narasi}`));
}

async function uploadFlow(id: string): Promise<void> {
  const ready = PLATFORMS.filter((p) => p.ready());
  const notReady = PLATFORMS.filter((p) => !p.ready()).map((p) => p.name);
  if (notReady.length) log(`   Belum dikonfigurasi (lihat README): ${notReady.join(", ")}`);
  if (!ready.length) {
    log("   Tidak ada platform yang siap. Video tetap tersimpan di: " + path.join(jobDirOf(id), "video.mp4"));
    return;
  }
  ready.forEach((p, i) => log(`   ${i + 1}. ${p.name}`));
  const pick = ((await question("   Upload ke mana? (contoh: 1,3 atau 'semua', kosong = batal): ")) ?? "").toLowerCase();
  if (!pick) return;
  const chosen = pick === "semua" ? ready : pick.split(",").map((n) => ready[Number(n.trim()) - 1]).filter(Boolean);
  await uploadJob(id, chosen.map((p) => p.name), reporter);
}

/** Produksi + review interaktif di terminal. */
async function produceAndReview(plan: Plan, prompt: string, cfg: Partial<VideoConfig>) {
  printPlan(plan);
  const id = await produce(plan, prompt, cfg, reporter);
  const outFile = path.join(jobDirOf(id), "video.mp4");
  for (;;) {
    log(`\nVideo selesai: ${outFile}`);
    openFile(outFile);
    const a = ((await question("\n[u] upload  [r] revisi animasi  [s] selesai tanpa upload : ")) ?? "s").toLowerCase();
    if (a === "u") {
      await uploadFlow(id);
      break;
    }
    if (a === "r") {
      const fb = (await question("Apa yang perlu diubah? ")) ?? "";
      if (fb) await revise(id, fb, reporter);
      continue;
    }
    if (a === "s") break;
  }
  log(`\nSemua file job ada di: ${jobDirOf(id)}`);
}

const modeLine = () => log(`Mode Claude: ${USE_API ? "API key (bayar per pemakaian)" : "Claude Code (langganan Anda)"}`);

async function cmdBuat(prompt: string) {
  if (!prompt) throw new Error('Tulis perintahnya, contoh: npm run buat -- "buatkan video tentang cara kerja termos"');
  modeLine();
  log("1) Claude menyusun naskah...");
  const plan = await makePlan(prompt, DEFAULT_CONFIG);
  await produceAndReview(plan, prompt, DEFAULT_CONFIG);
}

/** Buat ulang video dari naskah job lama (suara, animasi, render baru) ke folder job baru. */
async function cmdUlang(id: string) {
  if (!id) throw new Error("Tulis nama folder job, contoh: npm run ulang -- 20261117-083813-rahasia-termos-...");
  const oldDir = jobDirOf(id);
  const planFile = path.join(oldDir, "plan.json");
  if (!fs.existsSync(planFile)) throw new Error(`Job tidak ditemukan: ${oldDir}`);
  const plan: Plan = JSON.parse(fs.readFileSync(planFile, "utf8"));
  const promptFile = path.join(oldDir, "prompt.txt");
  const prompt = fs.existsSync(promptFile) ? fs.readFileSync(promptFile, "utf8") : plan.judul;
  const cfgFile = path.join(oldDir, "config.json");
  const cfg = normalizeConfig(fs.existsSync(cfgFile) ? JSON.parse(fs.readFileSync(cfgFile, "utf8")) : { topik: prompt });
  modeLine();
  log(`1) Memakai naskah dari ${path.basename(oldDir)}`);
  await produceAndReview(plan, prompt, cfg);
}

async function cmdUpload(id: string) {
  const planFile = path.join(jobDirOf(id), "plan.json");
  if (!fs.existsSync(planFile)) throw new Error(`Job tidak ditemukan: ${jobDirOf(id)}`);
  printPlan(JSON.parse(fs.readFileSync(planFile, "utf8")));
  await uploadFlow(path.basename(jobDirOf(id)));
}

async function main() {
  const [cmd, ...rest] = process.argv.slice(2);
  try {
    if (cmd === "buat") await cmdBuat(rest.join(" ").trim());
    else if (cmd === "upload") await cmdUpload(rest.join(" ").trim());
    else if (cmd === "ulang") await cmdUlang(rest.join(" ").trim());
    else
      log(
        'Pemakaian:\n  npm run buat -- "buatkan video tentang ..."\n  npm run ulang -- <nama-folder-job>\n  npm run upload -- <nama-folder-job>\n  npm run studio   (aplikasi web)',
      );
  } catch (e) {
    console.error(`\nERROR: ${(e as Error).message}`);
    process.exitCode = 1;
  } finally {
    rl.close();
  }
}

main();
