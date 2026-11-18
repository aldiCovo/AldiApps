// Verifikasi login Cloudflare Access (header Cf-Access-Jwt-Assertion, RS256) untuk pemilik Studio.
// Hanya pemilik yang lolos policy Access, jadi token Access yang sah dari tim ini = pemilik.
import crypto from "node:crypto";
import type http from "node:http";
import { env } from "./env";

type Jwk = crypto.JsonWebKey & { kid?: string };
let certs: { at: number; keys: Jwk[] } | null = null;

const team = () => env("CF_ACCESS_TEAM"); // mis. proud-flower-0355
const issuer = () => `https://${team()}.cloudflareaccess.com`;

async function keys(force = false): Promise<Jwk[]> {
  if (!force && certs && Date.now() - certs.at < 3600_000) return certs.keys;
  const r = await fetch(`${issuer()}/cdn-cgi/access/certs`);
  if (!r.ok) throw new Error(`certs Access HTTP ${r.status}`);
  const j = (await r.json()) as { keys?: Jwk[] };
  certs = { at: Date.now(), keys: j.keys ?? [] };
  return certs.keys;
}

const b64json = (s: string) => JSON.parse(Buffer.from(s, "base64url").toString("utf8"));

/** true jika permintaan membawa token Cloudflare Access yang sah dari tim ini. */
export async function viaAccess(req: http.IncomingMessage): Promise<boolean> {
  if (env("ADMIN_WITHOUT_ACCESS") === "1") return true; // hanya untuk uji lokal
  const token = String(req.headers["cf-access-jwt-assertion"] ?? "");
  if (!token || !team()) return false;
  try {
    const [h, p, sig] = token.split(".");
    const header = b64json(h);
    const payload = b64json(p);
    if (header.alg !== "RS256") return false;
    if (payload.iss !== issuer() || !(payload.exp * 1000 > Date.now())) return false;
    let jwk = (await keys()).find((k) => k.kid === header.kid);
    if (!jwk) jwk = (await keys(true)).find((k) => k.kid === header.kid);
    if (!jwk) return false;
    const key = crypto.createPublicKey({ key: jwk, format: "jwk" });
    return crypto.verify("RSA-SHA256", Buffer.from(`${h}.${p}`), key, Buffer.from(sig, "base64url"));
  } catch (e) {
    console.error("Verifikasi Cloudflare Access gagal:", (e as Error).message);
    return false;
  }
}
