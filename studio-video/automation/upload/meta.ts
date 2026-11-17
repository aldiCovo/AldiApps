import fs from "node:fs";
import { env } from "../env";
import { fetchJson, sleep, type UploadInput } from "./types";

// Instagram Reels & Facebook Page Reels lewat Meta Graph API (upload file langsung, tanpa hosting publik).
const V = env("META_GRAPH_VERSION", "v21.0");
const GRAPH = `https://graph.facebook.com/${V}`;

export const instagramReady = () => Boolean(env("META_PAGE_TOKEN") && env("IG_USER_ID"));
export const facebookReady = () => Boolean(env("META_PAGE_TOKEN") && env("FB_PAGE_ID"));

async function ruploadFile(uploadUrl: string, file: string, token: string): Promise<void> {
  const video = fs.readFileSync(file);
  const res = await fetch(uploadUrl, {
    method: "POST",
    headers: { Authorization: `OAuth ${token}`, offset: "0", file_size: String(video.length) },
    body: video,
  });
  if (!res.ok) throw new Error(`Upload ke Meta gagal: HTTP ${res.status} ${await res.text()}`);
}

export async function uploadInstagram(input: UploadInput): Promise<string> {
  const token = env("META_PAGE_TOKEN");
  const ig = env("IG_USER_ID");
  const params = new URLSearchParams({
    media_type: "REELS",
    upload_type: "resumable",
    caption: input.caption,
    share_to_feed: "true",
    access_token: token,
  });
  const container = await fetchJson(`${GRAPH}/${ig}/media?${params}`, { method: "POST" });
  if (!container.uri) throw new Error(`Container IG gagal: ${JSON.stringify(container)}`);
  await ruploadFile(container.uri, input.file, token);

  // Tunggu Instagram selesai memproses video.
  for (let i = 0; i < 60; i++) {
    await sleep(5000);
    const st = await fetchJson(`${GRAPH}/${container.id}?fields=status_code,status&access_token=${token}`);
    if (st.status_code === "FINISHED") break;
    if (st.status_code === "ERROR" || st.status_code === "EXPIRED") throw new Error(`Instagram gagal memproses: ${st.status}`);
    if (i === 59) throw new Error("Instagram terlalu lama memproses video.");
  }
  const pub = await fetchJson(
    `${GRAPH}/${ig}/media_publish?creation_id=${container.id}&access_token=${token}`,
    { method: "POST" },
  );
  const link = await fetchJson(`${GRAPH}/${pub.id}?fields=permalink&access_token=${token}`).catch(() => ({}));
  return link.permalink ?? `Instagram media id ${pub.id}`;
}

export async function uploadFacebook(input: UploadInput): Promise<string> {
  const token = env("META_PAGE_TOKEN");
  const page = env("FB_PAGE_ID");
  const start = await fetchJson(`${GRAPH}/${page}/video_reels`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ upload_phase: "start", access_token: token }),
  });
  if (!start.video_id) throw new Error(`Start Reels Facebook gagal: ${JSON.stringify(start)}`);
  const uploadUrl = start.upload_url ?? `https://rupload.facebook.com/video-upload/${V}/${start.video_id}`;
  await ruploadFile(uploadUrl, input.file, token);

  const finish = await fetchJson(`${GRAPH}/${page}/video_reels`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      upload_phase: "finish",
      video_id: start.video_id,
      video_state: env("FB_VIDEO_STATE", "PUBLISHED"),
      description: input.caption,
      access_token: token,
    }),
  });
  if (finish.success === false) throw new Error(`Finish Reels Facebook gagal: ${JSON.stringify(finish)}`);
  return `https://www.facebook.com/reel/${start.video_id}`;
}
