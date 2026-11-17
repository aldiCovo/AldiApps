import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import type { JobData } from "../src/auto/types";
import { ROOT } from "./env";

const GEN_DIR = path.join(ROOT, "src", "generated");

export type RenderProgress = (stage: "render" | "encode", done: number, total: number) => void;

/** Jalankan perintah, kembalikan kode keluar + output gabungan. */
function run(cmd: string, args: string[], onProgress?: RenderProgress): Promise<{ code: number; output: string }> {
  return new Promise((resolve) => {
    const p = spawn(cmd, args, { cwd: ROOT, shell: true });
    let output = "";
    const onData = (d: Buffer) => {
      const s = d.toString();
      output += s;
      if (onProgress) {
        const all = [...s.matchAll(/(Rendered|Encoded) (\d+)\/(\d+)/g)];
        const m = all[all.length - 1];
        if (m) onProgress(m[1] === "Rendered" ? "render" : "encode", Number(m[2]), Number(m[3]));
      }
    };
    p.stdout.on("data", onData);
    p.stderr.on("data", onData);
    p.on("close", (code) => resolve({ code: code ?? 1, output }));
  });
}

/** Tulis data job + kode scene ke src/generated agar dipakai komposisi AutoVideo. */
export function writeGenerated(job: JobData, sceneCode: string): void {
  fs.writeFileSync(
    path.join(GEN_DIR, "job.ts"),
    `import type { JobData } from "../auto/types";\n\nexport const job: JobData = ${JSON.stringify(job, null, 2)};\n`,
  );
  fs.writeFileSync(path.join(GEN_DIR, "Scenes.tsx"), sceneCode);
}

/** Cek tipe TypeScript. Mengembalikan pesan error, atau null jika lolos. */
export async function typecheck(): Promise<string | null> {
  const r = await run("npx", ["tsc", "--noEmit", "-p", "tsconfig.json"]);
  return r.code === 0 ? null : r.output;
}

/** Render komposisi AutoVideo ke file mp4. Mengembalikan pesan error, atau null jika sukses. */
export async function render(outFile: string, onProgress?: RenderProgress, entry = "src/index.ts"): Promise<string | null> {
  const r = await run(
    "npx",
    ["remotion", "render", entry, "AutoVideo", `"${outFile}"`, "--codec=h264", "--audio-codec=aac", "--pixel-format=yuv420p"],
    onProgress,
  );
  if (r.code === 0 && fs.existsSync(outFile)) return null;
  return r.output.split(/\r?\n/).filter((l) => !/^(Rendered|Encoded) \d+/.test(l.trim())).slice(-80).join("\n");
}
