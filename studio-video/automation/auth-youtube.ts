// Login YouTube sekali untuk mendapatkan refresh token (disimpan ke .env).
import http from "node:http";
import { spawn } from "node:child_process";
import { env, setEnv } from "./env";
import { YT_REDIRECT, youtubeOAuth } from "./upload/youtube";

if (!env("YOUTUBE_CLIENT_ID") || !env("YOUTUBE_CLIENT_SECRET")) {
  console.error("Isi YOUTUBE_CLIENT_ID dan YOUTUBE_CLIENT_SECRET di .env dulu (lihat README).");
  process.exit(1);
}

const oauth = youtubeOAuth();
const url = oauth.generateAuthUrl({
  access_type: "offline",
  prompt: "consent",
  scope: ["https://www.googleapis.com/auth/youtube.upload"],
});

const server = http.createServer(async (req, res) => {
  const code = new URL(req.url ?? "/", YT_REDIRECT).searchParams.get("code");
  if (!code) {
    res.end("Tidak ada kode.");
    return;
  }
  try {
    const { tokens } = await oauth.getToken(code);
    if (!tokens.refresh_token) throw new Error("Google tidak mengirim refresh token. Coba hapus akses app di akun Google lalu ulangi.");
    setEnv("YOUTUBE_REFRESH_TOKEN", tokens.refresh_token);
    res.end("Login YouTube berhasil. Tutup tab ini.");
    console.log("Berhasil! YOUTUBE_REFRESH_TOKEN disimpan ke .env");
  } catch (e) {
    res.end("Gagal: " + (e as Error).message);
    console.error((e as Error).message);
  }
  server.close();
});

server.listen(53682, () => {
  console.log("Membuka browser untuk login Google...\nJika tidak terbuka, buka URL ini:\n" + url);
  spawn("cmd", ["/c", "start", '""', `"${url}"`], { shell: true, detached: true });
});
