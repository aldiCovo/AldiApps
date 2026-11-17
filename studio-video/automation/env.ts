import fs from "node:fs";
import path from "node:path";
import dotenv from "dotenv";

export const ROOT = process.cwd();
const ENV_PATH = path.join(ROOT, ".env");

dotenv.config({ path: ENV_PATH, quiet: true });

export const env = (key: string, fallback = ""): string => process.env[key]?.trim() || fallback;

/** Simpan/ubah satu nilai di file .env (dipakai skrip login untuk menyimpan token). */
export const setEnv = (key: string, value: string): void => {
  const lines = fs.existsSync(ENV_PATH) ? fs.readFileSync(ENV_PATH, "utf8").split(/\r?\n/) : [];
  const idx = lines.findIndex((l) => l.startsWith(`${key}=`));
  if (idx >= 0) lines[idx] = `${key}=${value}`;
  else lines.push(`${key}=${value}`);
  fs.writeFileSync(ENV_PATH, lines.join("\n"));
  process.env[key] = value;
};
