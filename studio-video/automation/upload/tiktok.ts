import fs from "node:fs";
import { env, setEnv } from "../env";
import { fetchJson, sleep, type UploadInput } from "./types";

const API = "https://open.tiktokapis.com/v2";

// Hanya yang dipakai: profil dasar (login) dan Direct Post.
export const TIKTOK_SCOPE = "user.info.basic,video.publish";

export const tiktokKeys = () => Boolean(env("TIKTOK_CLIENT_KEY") && env("TIKTOK_CLIENT_SECRET"));
export const tiktokReady = () => tiktokKeys() && Boolean(env("TIKTOK_REFRESH_TOKEN"));

export function tiktokAuthorizeUrl(redirectUri: string, state: string): string {
  return (
    "https://www.tiktok.com/v2/auth/authorize/?" +
    new URLSearchParams({ client_key: env("TIKTOK_CLIENT_KEY"), scope: TIKTOK_SCOPE, response_type: "code", redirect_uri: redirectUri, state })
  );
}

export type TikTokTokens = {
  openId: string;
  accessToken: string;
  refreshToken: string;
  refreshExpiresAt: number; // ms
  scope: string;
};

/** Tukar kode dari halaman izin TikTok dengan token akun yang login. */
export async function tiktokExchangeCode(code: string, redirectUri: string): Promise<TikTokTokens> {
  const res = await fetch(`${API}/oauth/token/`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      client_key: env("TIKTOK_CLIENT_KEY"),
      client_secret: env("TIKTOK_CLIENT_SECRET"),
      code,
      grant_type: "authorization_code",
      redirect_uri: redirectUri,
    }),
  });
  const data: any = await res.json().catch(() => ({}));
  if (!data.refresh_token || !data.open_id) throw new Error(data.error_description || data.error || `HTTP ${res.status}`);
  return {
    openId: String(data.open_id),
    accessToken: String(data.access_token),
    refreshToken: String(data.refresh_token),
    refreshExpiresAt: Date.now() + Number(data.refresh_expires_in ?? 0) * 1000,
    scope: String(data.scope ?? ""),
  };
}

/** Nama dan foto profil akun TikTok (scope user.info.basic). */
export async function tiktokProfile(accessToken: string): Promise<{ nama: string; avatar: string }> {
  const r = await fetchJson(`${API}/user/info/?fields=open_id,display_name,avatar_url`, {
    headers: { Authorization: `Bearer ${accessToken}` },
  });
  const u = r.data?.user ?? {};
  return { nama: String(u.display_name ?? "Kreator TikTok").slice(0, 80), avatar: String(u.avatar_url ?? "") };
}

/** Access token TikTok berlaku 24 jam, jadi selalu diperbarui dari refresh token. */
async function accessToken(): Promise<string> {
  const body = new URLSearchParams({
    client_key: env("TIKTOK_CLIENT_KEY"),
    client_secret: env("TIKTOK_CLIENT_SECRET"),
    grant_type: "refresh_token",
    refresh_token: env("TIKTOK_REFRESH_TOKEN"),
  });
  const r = await fetchJson(`${API}/oauth/token/`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body,
  });
  if (!r.access_token) throw new Error(`Refresh token TikTok gagal: ${JSON.stringify(r)}`);
  if (r.refresh_token && r.refresh_token !== env("TIKTOK_REFRESH_TOKEN")) setEnv("TIKTOK_REFRESH_TOKEN", r.refresh_token);
  return r.access_token;
}

export async function uploadTikTok(input: UploadInput): Promise<string> {
  const token = await accessToken();
  const auth = { Authorization: `Bearer ${token}`, "Content-Type": "application/json; charset=UTF-8" };

  // Wajib menurut pedoman TikTok: cek opsi privasi yang diizinkan untuk akun ini.
  const info = await fetchJson(`${API}/post/publish/creator_info/query/`, { method: "POST", headers: auth });
  const allowed: string[] = info.data?.privacy_level_options ?? [];
  let privacy = env("TIKTOK_PRIVACY", "SELF_ONLY");
  if (allowed.length && !allowed.includes(privacy)) {
    console.log(`   TikTok: privasi ${privacy} tidak diizinkan untuk app ini, pakai ${allowed.includes("SELF_ONLY") ? "SELF_ONLY" : allowed[0]}`);
    privacy = allowed.includes("SELF_ONLY") ? "SELF_ONLY" : allowed[0];
  }

  const video = fs.readFileSync(input.file);
  const size = video.length;
  const MAX_SINGLE = 64 * 1024 * 1024;
  const chunkSize = size <= MAX_SINGLE ? size : 10 * 1024 * 1024;
  const totalChunks = size <= MAX_SINGLE ? 1 : Math.floor(size / chunkSize);

  const init = await fetchJson(`${API}/post/publish/video/init/`, {
    method: "POST",
    headers: auth,
    body: JSON.stringify({
      post_info: {
        title: input.caption.slice(0, 2200),
        privacy_level: privacy,
        disable_comment: false,
        disable_duet: false,
        disable_stitch: false,
        is_aigc: true, // label konten dibuat dengan AI
      },
      source_info: { source: "FILE_UPLOAD", video_size: size, chunk_size: chunkSize, total_chunk_count: totalChunks },
    }),
  });
  const { publish_id, upload_url } = init.data ?? {};
  if (!upload_url) throw new Error(`Init TikTok gagal: ${JSON.stringify(init)}`);

  for (let i = 0; i < totalChunks; i++) {
    const start = i * chunkSize;
    const end = i === totalChunks - 1 ? size : start + chunkSize; // potongan terakhir menampung sisa
    const res = await fetch(upload_url, {
      method: "PUT",
      headers: {
        "Content-Type": "video/mp4",
        "Content-Length": String(end - start),
        "Content-Range": `bytes ${start}-${end - 1}/${size}`,
      },
      body: video.subarray(start, end),
    });
    if (!res.ok) throw new Error(`Upload potongan TikTok gagal: HTTP ${res.status} ${await res.text()}`);
  }

  for (let i = 0; i < 60; i++) {
    await sleep(5000);
    const st = await fetchJson(`${API}/post/publish/status/fetch/`, {
      method: "POST",
      headers: auth,
      body: JSON.stringify({ publish_id }),
    });
    const status = st.data?.status;
    if (status === "PUBLISH_COMPLETE") return `TikTok publish_id ${publish_id} (privasi: ${privacy})`;
    if (status === "FAILED") throw new Error(`TikTok gagal memproses: ${st.data?.fail_reason}`);
  }
  return `TikTok publish_id ${publish_id} masih diproses (cek di aplikasi TikTok)`;
}
