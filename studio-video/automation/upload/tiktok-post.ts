// Direct Post ke akun TikTok pengguna yang login, sesuai Content Sharing Guidelines TikTok:
// info kreator dibaca dulu, pengguna memilih privasi dan izin interaksi sendiri, video diambil TikTok
// dari URL di domain terverifikasi (PULL_FROM_URL), lalu status posting dipantau.
import { env } from "../env";
import { getUser, updateUser } from "../users";
import { sleep } from "./types";

const API = "https://open.tiktokapis.com/v2";

/** Error yang artinya pengguna harus login TikTok ulang (token habis atau izin kurang). */
export class NeedsLogin extends Error {}

type TikTokError = { code?: string; message?: string; log_id?: string };

/** Panggil API TikTok; lempar error yang bisa dibaca jika status bukan 2xx atau error.code bukan "ok". */
async function call<T = any>(url: string, token: string, body?: unknown): Promise<T> {
  const res = await fetch(url, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json; charset=UTF-8" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const j: { data?: T; error?: TikTokError } = await res.json().catch(() => ({}));
  const code = j.error?.code ?? (res.ok ? "ok" : `http_${res.status}`);
  if (code !== "ok") {
    if (code === "access_token_invalid" || code === "scope_not_authorized") throw new NeedsLogin(MESSAGES[code]);
    throw new Error(MESSAGES[code] ?? `TikTok: ${j.error?.message || code}`);
  }
  return (j.data ?? {}) as T;
}

const MESSAGES: Record<string, string> = {
  access_token_invalid: "Login TikTok sudah kedaluwarsa. Hubungkan ulang akun TikTok.",
  scope_not_authorized: "Studio belum diberi izin memosting video. Hubungkan ulang akun TikTok dan izinkan posting.",
  spam_risk_too_many_posts: "Akun ini sudah mencapai batas posting harian TikTok. Coba lagi besok.",
  spam_risk_user_banned_from_posting: "TikTok sedang membatasi akun ini untuk memosting. Coba lagi nanti.",
  reached_active_user_cap: "Kuota harian app di TikTok sudah penuh. Coba lagi besok.",
  unaudited_client_can_only_post_to_private_accounts: "Sebelum app lolos audit TikTok, video hanya bisa diposting ke akun privat dengan privasi \"Hanya saya\".",
  privacy_level_option_mismatch: "Pilihan privasi tidak tersedia untuk akun ini.",
  url_ownership_unverified: "Domain video belum diverifikasi di app TikTok.",
};

/** Access token baru dari refresh token milik pengguna; refresh token yang diganti TikTok disimpan. */
export async function userAccessToken(openId: string): Promise<string> {
  const u = getUser(openId);
  if (!u?.tiktok?.refreshToken) throw new NeedsLogin("Akun TikTok belum terhubung.");
  if (!u.tiktok.scope.split(",").includes("video.publish")) throw new NeedsLogin(MESSAGES.scope_not_authorized);
  const res = await fetch(`${API}/oauth/token/`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      client_key: env("TIKTOK_CLIENT_KEY"),
      client_secret: env("TIKTOK_CLIENT_SECRET"),
      grant_type: "refresh_token",
      refresh_token: u.tiktok.refreshToken,
    }),
  });
  const d: any = await res.json().catch(() => ({}));
  if (!d.access_token) throw new NeedsLogin(MESSAGES.access_token_invalid);
  if (d.refresh_token && d.refresh_token !== u.tiktok.refreshToken) {
    updateUser(openId, {
      tiktok: {
        refreshToken: d.refresh_token,
        refreshExpiresAt: Date.now() + Number(d.refresh_expires_in ?? 0) * 1000,
        scope: String(d.scope ?? u.tiktok.scope),
      },
    });
  }
  return String(d.access_token);
}

export type CreatorInfo = {
  nickname: string;
  username: string;
  avatar: string;
  privacyOptions: string[];
  commentDisabled: boolean;
  duetDisabled: boolean;
  stitchDisabled: boolean;
  maxDurationSec: number;
};

export async function creatorInfo(token: string): Promise<CreatorInfo> {
  const d = await call(`${API}/post/publish/creator_info/query/`, token);
  return {
    nickname: String(d.creator_nickname ?? ""),
    username: String(d.creator_username ?? ""),
    avatar: String(d.creator_avatar_url ?? ""),
    privacyOptions: Array.isArray(d.privacy_level_options) ? d.privacy_level_options.map(String) : [],
    commentDisabled: Boolean(d.comment_disabled),
    duetDisabled: Boolean(d.duet_disabled),
    stitchDisabled: Boolean(d.stitch_disabled),
    maxDurationSec: Number(d.max_video_post_duration_sec ?? 0),
  };
}

export type PostOptions = {
  title: string;
  privacy: string;
  allowComment: boolean;
  allowDuet: boolean;
  allowStitch: boolean;
  commercial: boolean;
  yourBrand: boolean; // brand_organic_toggle: label "Promotional content"
  branded: boolean; // brand_content_toggle: label "Paid partnership"
};

export function parsePostOptions(b: Record<string, unknown>): PostOptions {
  const commercial = b.commercial === true;
  return {
    title: String(b.title ?? "").slice(0, 2200),
    privacy: String(b.privacy ?? ""),
    allowComment: b.allowComment === true,
    allowDuet: b.allowDuet === true,
    allowStitch: b.allowStitch === true,
    commercial,
    yourBrand: commercial && b.yourBrand === true,
    branded: commercial && b.branded === true,
  };
}

/** Aturan yang sama dengan halaman posting, dicek ulang di server. null = boleh diposting. */
export function checkPost(info: CreatorInfo, o: PostOptions, durationSec: number): string | null {
  if (!o.privacy) return "Pilih siapa yang bisa menonton video ini.";
  if (!info.privacyOptions.includes(o.privacy)) return MESSAGES.privacy_level_option_mismatch;
  if (info.maxDurationSec && durationSec > info.maxDurationSec)
    return `Video ${Math.ceil(durationSec)} detik, lebih panjang dari batas akun ini (${info.maxDurationSec} detik).`;
  if (o.commercial && !o.yourBrand && !o.branded) return "Pilih apakah konten ini mempromosikan brand Anda, pihak ketiga, atau keduanya.";
  if (o.branded && o.privacy === "SELF_ONLY") return "Konten bersponsor tidak bisa diposting dengan privasi \"Hanya saya\".";
  return null;
}

/** Mulai Direct Post; TikTok mengambil video dari `videoUrl`. Mengembalikan publish_id. */
export async function directPost(token: string, videoUrl: string, info: CreatorInfo, o: PostOptions): Promise<string> {
  const d = await call(`${API}/post/publish/video/init/`, token, {
    post_info: {
      title: o.title,
      privacy_level: o.privacy,
      // Izin yang dinonaktifkan di akun kreator selalu tetap nonaktif.
      disable_comment: info.commentDisabled || !o.allowComment,
      disable_duet: info.duetDisabled || !o.allowDuet,
      disable_stitch: info.stitchDisabled || !o.allowStitch,
      brand_organic_toggle: o.yourBrand,
      brand_content_toggle: o.branded,
      is_aigc: true, // label konten buatan AI
    },
    source_info: { source: "PULL_FROM_URL", video_url: videoUrl },
  });
  if (!d.publish_id) throw new Error("TikTok tidak mengembalikan publish_id.");
  return String(d.publish_id);
}

/** Pantau status sampai selesai (maks ~10 menit). */
export async function waitPublish(token: string, publishId: string, log: (m: string) => void): Promise<string> {
  let last = "";
  for (let i = 0; i < 120; i++) {
    await sleep(5000);
    const d = await call(`${API}/post/publish/status/fetch/`, token, { publish_id: publishId });
    const status = String(d.status ?? "");
    if (status !== last) {
      last = status;
      log(`TikTok: ${STATUS_LABEL[status] ?? status}`);
    }
    if (status === "PUBLISH_COMPLETE") return `Terposting (publish_id ${publishId})`;
    if (status === "FAILED") throw new Error(`TikTok gagal memproses video: ${d.fail_reason ?? "tanpa alasan"}`);
  }
  return `Masih diproses TikTok (publish_id ${publishId}). Cek profil TikTok beberapa menit lagi.`;
}

const STATUS_LABEL: Record<string, string> = {
  PROCESSING_DOWNLOAD: "mengambil video dari Studio...",
  PROCESSING_UPLOAD: "menerima video...",
  SEND_TO_USER_INBOX: "dikirim ke kotak masuk TikTok",
  PUBLISH_COMPLETE: "selesai diposting",
  FAILED: "gagal",
};
