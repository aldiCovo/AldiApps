// Login TikTok sekali untuk mendapatkan refresh token (disimpan ke .env).
import readline from "node:readline/promises";
import { spawn } from "node:child_process";
import { env, setEnv } from "./env";

const key = env("TIKTOK_CLIENT_KEY");
const secret = env("TIKTOK_CLIENT_SECRET");
const redirect = env("TIKTOK_REDIRECT_URI");
if (!key || !secret || !redirect) {
  console.error("Isi TIKTOK_CLIENT_KEY, TIKTOK_CLIENT_SECRET, dan TIKTOK_REDIRECT_URI di .env dulu (lihat README).");
  process.exit(1);
}

async function main() {
  const state = Math.random().toString(36).slice(2);
  const url =
    "https://www.tiktok.com/v2/auth/authorize/?" +
    new URLSearchParams({
      client_key: key,
      scope: "user.info.basic,video.publish,video.upload",
      response_type: "code",
      redirect_uri: redirect,
      state,
    });
  console.log("Login TikTok di browser. Setelah diizinkan, browser diarahkan ke redirect URI Anda.");
  console.log("Salin URL lengkap dari address bar (berisi ?code=...) lalu tempel di sini.\n" + url + "\n");
  spawn("cmd", ["/c", "start", '""', `"${url}"`], { shell: true, detached: true });

  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  const pasted = (await rl.question("URL hasil redirect: ")).trim();
  rl.close();
  const code = new URL(pasted).searchParams.get("code");
  if (!code) throw new Error("Tidak menemukan ?code= di URL tersebut.");

  const res = await fetch("https://open.tiktokapis.com/v2/oauth/token/", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      client_key: key,
      client_secret: secret,
      code: decodeURIComponent(code),
      grant_type: "authorization_code",
      redirect_uri: redirect,
    }),
  });
  const data: any = await res.json();
  if (!data.refresh_token) throw new Error("Gagal menukar kode: " + JSON.stringify(data));
  setEnv("TIKTOK_REFRESH_TOKEN", data.refresh_token);
  console.log("Berhasil! TIKTOK_REFRESH_TOKEN disimpan ke .env");
}

main().catch((e) => {
  console.error(e.message);
  process.exit(1);
});
