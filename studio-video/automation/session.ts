// Sesi login: cookie studio_sid = <openId base64url>.<kedaluwarsa ms>.<HMAC-SHA256>, ditandatangani SESSION_SECRET.
import crypto from "node:crypto";
import type http from "node:http";
import { env } from "./env";

const COOKIE = "studio_sid";
const MAX_AGE_S = 30 * 24 * 3600;

let devSecret: string | null = null;
function secret(): string {
  const s = env("SESSION_SECRET");
  if (s.length >= 32) return s;
  // Tanpa SESSION_SECRET (mis. di laptop), sesi hanya berlaku sampai Studio di-restart.
  if (!devSecret) {
    devSecret = crypto.randomBytes(32).toString("hex");
    console.warn("SESSION_SECRET kosong atau kurang dari 32 karakter: login hilang setiap Studio di-restart.");
  }
  return devSecret;
}

const mac = (data: string) => crypto.createHmac("sha256", secret()).update(data).digest("base64url");

function parseCookies(req: http.IncomingMessage): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of (req.headers.cookie ?? "").split(";")) {
    const i = part.indexOf("=");
    if (i > 0) out[part.slice(0, i).trim()] = part.slice(i + 1).trim();
  }
  return out;
}

/** openId pemilik sesi yang sah, atau null. */
export function sessionUser(req: http.IncomingMessage): string | null {
  const raw = parseCookies(req)[COOKIE];
  if (!raw) return null;
  const [id, exp, sig] = raw.split(".");
  if (!id || !exp || !sig) return null;
  const want = Buffer.from(mac(`${id}.${exp}`));
  const got = Buffer.from(sig);
  if (got.length !== want.length || !crypto.timingSafeEqual(got, want)) return null;
  if (!(Number(exp) > Date.now())) return null;
  return Buffer.from(id, "base64url").toString("utf8") || null;
}

const secure = (req: http.IncomingMessage) => req.headers["x-forwarded-proto"] === "https" || /\.aldiab\.ai\.id$/.test(String(req.headers.host ?? ""));

export function sessionCookie(req: http.IncomingMessage, openId: string): string {
  const id = Buffer.from(openId, "utf8").toString("base64url");
  const exp = String(Date.now() + MAX_AGE_S * 1000);
  return `${COOKIE}=${id}.${exp}.${mac(`${id}.${exp}`)}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${MAX_AGE_S}${secure(req) ? "; Secure" : ""}`;
}

export const clearCookie = (req: http.IncomingMessage) =>
  `${COOKIE}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0${secure(req) ? "; Secure" : ""}`;

/**
 * Anti-CSRF untuk POST dari browser: header Origin wajib ada dan sama dengan host yang diminta.
 * (Endpoint worker tidak memakai ini; worker memakai token.)
 */
export function sameOrigin(req: http.IncomingMessage): boolean {
  const origin = req.headers.origin;
  if (!origin) return false;
  try {
    return new URL(origin).host === req.headers.host;
  } catch {
    return false;
  }
}
