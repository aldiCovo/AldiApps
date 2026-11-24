// Pengguna Studio (login TikTok) dan kode undangan, disimpan sebagai JSON di jobs/_data (ada di volume server).
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { ROOT } from "./env";

export const DATA_DIR = path.join(ROOT, "jobs", "_data");
const USERS = path.join(DATA_DIR, "users.json");
const INVITES = path.join(DATA_DIR, "invites.json");

export type Role = "owner" | "creator";
export type TikTokAuth = { refreshToken: string; refreshExpiresAt: number; scope: string };
export type User = {
  openId: string;
  nama: string;
  avatar: string;
  role: Role;
  limitUsd: number; // batas biaya AI per bulan (kreator); owner tidak dibatasi
  tiktok: TikTokAuth | null;
  invite: string | null; // kode undangan yang dipakai saat mendaftar
  createdAt: string;
  lastLoginAt: string;
  disabled: boolean;
};
export type Invite = { code: string; catatan: string; createdAt: string; usedBy: string | null; usedAt: string | null; revoked: boolean };

export const DEFAULT_LIMIT_USD = 10;

function read<T>(file: string): T[] {
  try {
    const v = JSON.parse(fs.readFileSync(file, "utf8"));
    return Array.isArray(v) ? v : [];
  } catch {
    return [];
  }
}

/** Tulis ke file sementara lalu rename, agar file tidak pernah setengah tertulis. */
function write(file: string, data: unknown) {
  fs.mkdirSync(DATA_DIR, { recursive: true });
  const tmp = `${file}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(data, null, 2), { mode: 0o600 });
  fs.renameSync(tmp, file);
}

export const listUsers = () => read<User>(USERS);
export const getUser = (openId: string) => listUsers().find((u) => u.openId === openId) ?? null;
export const owner = () => listUsers().find((u) => u.role === "owner") ?? null;

/** Ubah satu user (atau tambahkan bila belum ada). Mengembalikan user hasil. */
export function saveUser(u: User): User {
  const all = listUsers();
  const i = all.findIndex((x) => x.openId === u.openId);
  if (i >= 0) all[i] = u;
  else all.push(u);
  write(USERS, all);
  return u;
}

export function updateUser(openId: string, patch: Partial<Omit<User, "openId">>): User | null {
  const u = getUser(openId);
  return u ? saveUser({ ...u, ...patch }) : null;
}

export function newUser(openId: string, nama: string, avatar: string, role: Role, invite: string | null, tiktok: TikTokAuth | null): User {
  const now = new Date().toISOString();
  return saveUser({ openId, nama, avatar, role, limitUsd: DEFAULT_LIMIT_USD, tiktok, invite, createdAt: now, lastLoginAt: now, disabled: false });
}

/** Data user yang aman dikirim ke browser (tanpa token). */
export const publicUser = (u: User) => ({ openId: u.openId, nama: u.nama, avatar: u.avatar, role: u.role, limitUsd: u.limitUsd });

/* ---------------- Undangan ---------------- */

export const listInvites = () => read<Invite>(INVITES);

// Tanpa huruf/angka yang mirip (0/O, 1/I/L), agar mudah diketik ulang.
const ALPHABET = "ABCDEFGHJKMNPQRSTUVWXYZ23456789";
const newCode = () => Array.from(crypto.randomBytes(10), (b) => ALPHABET[b % ALPHABET.length]).join("");

export function createInvite(catatan: string): Invite {
  const inv: Invite = { code: newCode(), catatan: catatan.slice(0, 200), createdAt: new Date().toISOString(), usedBy: null, usedAt: null, revoked: false };
  write(INVITES, [...listInvites(), inv]);
  return inv;
}

export const normalizeCode = (c: string) => c.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 20);

export function inviteUsable(code: string): boolean {
  const inv = listInvites().find((i) => i.code === normalizeCode(code));
  return Boolean(inv && !inv.usedBy && !inv.revoked);
}

/** Pakai undangan untuk openId. false jika kode tidak ada, sudah dipakai, atau dicabut. */
export function takeInvite(code: string, openId: string): boolean {
  const all = listInvites();
  const inv = all.find((i) => i.code === normalizeCode(code));
  if (!inv || inv.usedBy || inv.revoked) return false;
  inv.usedBy = openId;
  inv.usedAt = new Date().toISOString();
  write(INVITES, all);
  return true;
}

export function revokeInvite(code: string): boolean {
  const all = listInvites();
  const inv = all.find((i) => i.code === normalizeCode(code));
  if (!inv || inv.usedBy) return false;
  inv.revoked = true;
  write(INVITES, all);
  return true;
}
