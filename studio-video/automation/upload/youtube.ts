import fs from "node:fs";
import { google } from "googleapis";
import { env } from "../env";
import type { UploadInput } from "./types";

export const YT_REDIRECT = "http://localhost:53682";

export const youtubeOAuth = () =>
  new google.auth.OAuth2(env("YOUTUBE_CLIENT_ID"), env("YOUTUBE_CLIENT_SECRET"), YT_REDIRECT);

export const youtubeReady = () =>
  Boolean(env("YOUTUBE_CLIENT_ID") && env("YOUTUBE_CLIENT_SECRET") && env("YOUTUBE_REFRESH_TOKEN"));

export async function uploadYouTube(input: UploadInput): Promise<string> {
  const auth = youtubeOAuth();
  auth.setCredentials({ refresh_token: env("YOUTUBE_REFRESH_TOKEN") });
  const yt = google.youtube({ version: "v3", auth });
  const title = input.title.includes("#Shorts") ? input.title : `${input.title} #Shorts`;
  const res = await yt.videos.insert({
    part: ["snippet", "status"],
    requestBody: {
      snippet: {
        title: title.slice(0, 100),
        description: input.description,
        tags: input.tags,
        categoryId: "27", // Education
        defaultLanguage: "id",
        defaultAudioLanguage: "id",
      },
      status: {
        privacyStatus: env("YOUTUBE_PRIVACY", "public"),
        selfDeclaredMadeForKids: false,
      },
    },
    media: { body: fs.createReadStream(input.file) },
  });
  return `https://youtube.com/shorts/${res.data.id}`;
}
