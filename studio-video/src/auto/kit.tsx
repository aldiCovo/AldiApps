// Kit motion design untuk video explainer vertikal (gaya kinetik: shot cepat, teks besar, kartu UI).
// Dipakai oleh kode yang ditulis Claude di src/generated/Scenes.tsx.
// Semua komponen memakai useCurrentFrame() lokal, jadi `delay` dihitung dari awal shot/Sequence tempat ia berada.
import React, { createContext, useContext } from "react";
import {
  AbsoluteFill,
  Audio,
  Easing,
  Sequence,
  interpolate,
  random,
  spring,
  staticFile,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";
import type { SceneWord } from "./types";

export const W = 1080;
export const H = 1920;
export const FONT = '"Segoe UI", Arial, sans-serif';
export const MONO = '"Cascadia Mono", Consolas, monospace';
export const CLAMP = { extrapolateLeft: "clamp", extrapolateRight: "clamp" } as const;

/* ============================== Tema ============================== */

export type ThemeName = "dark" | "cream" | "accent" | "night";
export type Theme = {
  bg: string;
  bg2: string;
  fg: string;
  muted: string;
  card: string;
  cardBorder: string;
  accent: string;
  accent2: string;
  grid: string;
  glow: string;
  shadow: string;
  vignette: number;
};

export const THEMES: Record<ThemeName, Theme> = {
  dark: {
    bg: "#0c0c10", bg2: "#17161d", fg: "#ffffff", muted: "#8d8b99", card: "#1c1b23",
    cardBorder: "rgba(255,255,255,0.09)", accent: "#ff5a36", accent2: "#ffc53d",
    grid: "rgba(255,255,255,0.045)", glow: "rgba(255,90,54,0.22)", shadow: "0 30px 80px rgba(0,0,0,0.55)", vignette: 0.35,
  },
  cream: {
    bg: "#f3f0ea", bg2: "#e9e4da", fg: "#141414", muted: "#7a756c", card: "#ffffff",
    cardBorder: "rgba(0,0,0,0.07)", accent: "#ff5a36", accent2: "#2f6bff",
    grid: "rgba(0,0,0,0.055)", glow: "rgba(255,90,54,0.13)", shadow: "0 24px 60px rgba(60,40,20,0.16)", vignette: 0.06,
  },
  accent: {
    bg: "#ff5a36", bg2: "#ff8d4d", fg: "#ffffff", muted: "rgba(255,255,255,0.78)", card: "rgba(255,255,255,0.16)",
    cardBorder: "rgba(255,255,255,0.32)", accent: "#1a1a1a", accent2: "#ffe08a",
    grid: "rgba(255,255,255,0.09)", glow: "rgba(255,235,190,0.4)", shadow: "0 30px 70px rgba(120,20,0,0.35)", vignette: 0.15,
  },
  night: {
    bg: "#071022", bg2: "#10224a", fg: "#ffffff", muted: "#8ea3cc", card: "#13244d",
    cardBorder: "rgba(120,170,255,0.2)", accent: "#3fd0ff", accent2: "#ffd23f",
    grid: "rgba(120,170,255,0.06)", glow: "rgba(63,208,255,0.22)", shadow: "0 30px 80px rgba(0,0,0,0.5)", vignette: 0.35,
  },
};

const ThemeCtx = createContext<Theme>(THEMES.dark);
/** Warna tema dari Stage/Shots terdekat. */
export const useTheme = (): Theme => useContext(ThemeCtx);

/* ============================== Hook waktu ============================== */

/** Spring 0→1 yang mulai pada `delay` frame. */
export const useEnter = (delay = 0, config: { damping?: number; stiffness?: number; mass?: number } = {}): number => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  return spring({ frame: frame - delay, fps, config: { damping: 14, stiffness: 160, ...config } });
};

/** Progres linear (ber-easing) 0→1 dari `delay` selama `duration` frame. */
export const useProgress = (delay = 0, duration = 30, easing: (t: number) => number = Easing.out(Easing.cubic)): number => {
  const frame = useCurrentFrame();
  return interpolate(frame, [delay, delay + Math.max(1, duration)], [0, 1], { ...CLAMP, easing });
};

const norm = (s: string) => s.toLowerCase().replace(/[^\p{L}\p{N}]+/gu, "");

/** Frame saat `word` mulai diucapkan (kemunculan ke-`nth`). Jika tidak ditemukan, kembalikan `fallback`. */
export const wordAt = (words: SceneWord[], word: string, fallback: number, nth = 1): number => {
  const target = norm(word);
  let seen = 0;
  for (const w of words) {
    const n = norm(w.t);
    if (n === target || (target.length >= 4 && n.startsWith(target))) {
      seen++;
      if (seen === nth) return w.s;
    }
  }
  return fallback;
};

/* ============================== Panggung & Shot ============================== */

/** Latar penuh bertema: gradien, grid bergerak, cahaya melayang, vignette, dan dorongan kamera pelan. */
export const Stage: React.FC<{
  theme?: ThemeName;
  seed?: string;
  grid?: boolean;
  drift?: boolean;
  children?: React.ReactNode;
}> = ({ theme = "dark", seed = "stage", grid = true, drift = true, children }) => {
  const t = THEMES[theme];
  const f = useCurrentFrame();
  const r = random(seed);
  const gx = 50 + Math.sin(f / 55 + r * 6) * 28;
  const gy = 38 + Math.cos(f / 70 + r * 4) * 16;
  const zoom = drift ? interpolate(f, [0, 240], [1, 1.05], CLAMP) : 1;
  return (
    <ThemeCtx.Provider value={t}>
      <AbsoluteFill
        style={{ background: `linear-gradient(170deg, ${t.bg} 0%, ${t.bg2} 100%)`, fontFamily: FONT, color: t.fg, overflow: "hidden" }}
      >
        {grid ? (
          <AbsoluteFill
            style={{
              backgroundImage: `linear-gradient(${t.grid} 2px, transparent 2px), linear-gradient(90deg, ${t.grid} 2px, transparent 2px)`,
              backgroundSize: "90px 90px",
              backgroundPosition: `${-f * 0.35}px ${-f * 0.55}px`,
            }}
          />
        ) : null}
        <AbsoluteFill style={{ background: `radial-gradient(circle at ${gx}% ${gy}%, ${t.glow} 0%, rgba(0,0,0,0) 55%)` }} />
        <AbsoluteFill style={{ transform: `scale(${zoom})` }}>{children}</AbsoluteFill>
        <AbsoluteFill
          style={{ background: `radial-gradient(ellipse at center, rgba(0,0,0,0) 55%, rgba(0,0,0,${t.vignette}) 100%)` }}
        />
      </AbsoluteFill>
    </ThemeCtx.Provider>
  );
};

/** Efek masuk tiap shot: punch zoom + blur + geser. */
const ShotIn: React.FC<{ index: number; children?: React.ReactNode }> = ({ index, children }) => {
  const f = useCurrentFrame();
  if (index === 0) return <AbsoluteFill>{children}</AbsoluteFill>;
  const p = interpolate(f, [0, 8], [0, 1], { ...CLAMP, easing: Easing.out(Easing.cubic) });
  const dir = index % 2 === 0 ? 1 : -1;
  const flash = interpolate(f, [0, 5], [0.28, 0], CLAMP);
  return (
    <AbsoluteFill>
      <AbsoluteFill
        style={{
          transform: `translateX(${(1 - p) * 140 * dir}px) scale(${1.12 - 0.12 * p})`,
          filter: p < 1 ? `blur(${(1 - p) * 14}px)` : undefined,
        }}
      >
        {children}
      </AbsoluteFill>
      <AbsoluteFill style={{ background: "#ffffff", opacity: flash }} />
    </AbsoluteFill>
  );
};

export type Shot = { theme?: ThemeName; grid?: boolean; content: React.ReactNode };

/**
 * Pecah satu scene menjadi beberapa shot cepat (idealnya 1,5–3 detik per shot).
 * `cuts` = frame awal shot ke-2, ke-3, dst (panjang shots.length - 1), mis. dari wordAt(...).
 * Tanpa `cuts`, durasi dibagi rata. Tema default bergantian dark / cream.
 */
export const Shots: React.FC<{ durationInFrames: number; shots: Shot[]; cuts?: number[]; sfx?: boolean }> = ({
  durationInFrames,
  shots,
  cuts,
  sfx = true,
}) => {
  const n = Math.max(1, shots.length);
  let starts: number[] = Array.from({ length: n }, (_, i) => Math.round((i * durationInFrames) / n));
  if (cuts && cuts.length === n - 1) {
    starts = [0, ...cuts.map((c) => Math.round(c))];
    for (let i = 1; i < n; i++) {
      const minStart = starts[i - 1] + 12;
      const maxStart = durationInFrames - (n - i) * 12;
      starts[i] = Math.min(Math.max(starts[i], minStart), maxStart);
    }
  }
  return (
    <AbsoluteFill>
      {shots.map((s, i) => {
        const from = starts[i];
        const to = i < n - 1 ? starts[i + 1] : durationInFrames;
        return (
          <Sequence key={i} from={from} durationInFrames={Math.max(1, to - from)}>
            <ShotIn index={i}>
              <Stage theme={s.theme ?? (i % 2 === 0 ? "dark" : "cream")} seed={`shot-${i}`} grid={s.grid}>
                {s.content}
              </Stage>
            </ShotIn>
            {sfx && i > 0 ? <Audio src={staticFile("sfx/whoosh.wav")} volume={0.3} /> : null}
          </Sequence>
        );
      })}
    </AbsoluteFill>
  );
};

/** Efek suara pendek pada frame `at`. name: "pop" | "whoosh" | "tick". */
export const Sfx: React.FC<{ name: "pop" | "whoosh" | "tick"; at?: number; volume?: number }> = ({ name, at = 0, volume = 0.4 }) => (
  <Sequence from={Math.max(0, Math.round(at))} durationInFrames={20} layout="none">
    <Audio src={staticFile(`sfx/${name}.wav`)} volume={volume} />
  </Sequence>
);

/* ============================== Tata letak ============================== */

/** Ukuran kanvas video saat ini. */
export const useSize = (): { w: number; h: number } => {
  const { width, height } = useVideoConfig();
  return { w: width, h: height };
};

/** Area konten aman (di atas zona subtitle), isi ditumpuk vertikal di tengah. Menyesuaikan 9:16 atau 16:9. */
export const Safe: React.FC<{ children?: React.ReactNode; gap?: number; align?: "center" | "flex-start"; style?: React.CSSProperties }> = ({
  children,
  gap = 48,
  align = "center",
  style,
}) => {
  const { w, h } = useSize();
  const landscape = w > h;
  const top = landscape ? 70 : 180;
  const bottom = landscape ? h - 230 : h - 420;
  return (
    <div
      style={{
        position: "absolute",
        left: landscape ? 120 : 60,
        right: landscape ? 120 : 60,
        top,
        height: bottom - top,
        display: "flex",
        flexDirection: "column",
        alignItems: align,
        justifyContent: "center",
        gap,
        ...style,
      }}
    >
      {children}
    </div>
  );
};

/** Taruh anak dengan titik tengahnya di (x, y). */
export const At: React.FC<{ x: number; y: number; children?: React.ReactNode; style?: React.CSSProperties }> = ({ x, y, children, style }) => (
  <div style={{ position: "absolute", left: x, top: y, transform: "translate(-50%, -50%)", ...style }}>{children}</div>
);

export type Dir = "up" | "down" | "left" | "right" | "scale" | "none";

/** Entrance pegas generik. */
export const Pop: React.FC<{
  delay?: number;
  from?: Dir;
  distance?: number;
  float?: boolean;
  style?: React.CSSProperties;
  children?: React.ReactNode;
}> = ({ delay = 0, from = "up", distance = 80, float = false, style, children }) => {
  const f = useCurrentFrame();
  const e = useEnter(delay, { damping: 13, stiffness: 170 });
  const k = 1 - e;
  let tr = "";
  if (from === "up") tr = `translateY(${k * distance}px)`;
  if (from === "down") tr = `translateY(${-k * distance}px)`;
  if (from === "left") tr = `translateX(${-k * distance * 1.5}px)`;
  if (from === "right") tr = `translateX(${k * distance * 1.5}px)`;
  const scale = from === "scale" ? 0.4 + 0.6 * e : 0.92 + 0.08 * e;
  const bob = float ? Math.sin((f - delay) / 18) * 8 : 0;
  return (
    <div
      style={{
        opacity: from === "none" ? 1 : Math.min(1, e * 1.6),
        transform: `${tr} translateY(${bob}px) scale(${scale})`,
        filter: k > 0.02 ? `blur(${k * 8}px)` : undefined,
        ...style,
      }}
    >
      {children}
    </div>
  );
};

/* ============================== Tipografi ============================== */

/**
 * Teks kinetik: kata muncul satu per satu. Kata di dalam *bintang* diberi warna aksen. "\n" = baris baru.
 * Contoh: <Kinetic text={"BUANG WAKTU\nLEBIH DARI *DUA JAM*"} size={120} />
 */
export const Kinetic: React.FC<{
  text: string;
  delay?: number;
  stagger?: number;
  size?: number;
  weight?: number;
  color?: string;
  accent?: string;
  align?: "center" | "left";
  maxWidth?: number;
  lineHeight?: number;
  uppercase?: boolean;
  style?: React.CSSProperties;
}> = ({ text, delay = 0, stagger = 4, size = 110, weight = 900, color, accent, align = "center", maxWidth = 960, lineHeight = 1.04, uppercase = false, style }) => {
  const t = useTheme();
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const lines = text.split("\n");
  let idx = 0;
  let inAccent = false;
  return (
    <div
      style={{
        fontFamily: FONT,
        fontWeight: weight,
        fontSize: size,
        lineHeight,
        letterSpacing: "-0.02em",
        textAlign: align,
        maxWidth,
        color: color ?? t.fg,
        textTransform: uppercase ? "uppercase" : undefined,
        ...style,
      }}
    >
      {lines.map((line, li) => (
        <div key={li}>
          {line
            .split(/\s+/)
            .filter(Boolean)
            .map((raw, wi) => {
              const starts = raw.startsWith("*");
              const ends = raw.endsWith("*") && raw.length > 1;
              if (starts) inAccent = true;
              const isAccent = inAccent;
              if (ends) inAccent = false;
              const word = raw.replace(/\*/g, "");
              const i = idx++;
              const e = spring({ frame: frame - delay - i * stagger, fps, config: { damping: 13, stiffness: 190 } });
              const k = 1 - e;
              return (
                <span
                  key={wi}
                  style={{
                    display: "inline-block",
                    marginRight: "0.25em",
                    color: isAccent ? accent ?? t.accent : undefined,
                    opacity: Math.min(1, e * 1.7),
                    transform: `translateY(${k * 0.5 * size}px) scale(${0.75 + 0.25 * e})`,
                    filter: k > 0.02 ? `blur(${k * 10}px)` : undefined,
                  }}
                >
                  {word}
                </span>
              );
            })}
        </div>
      ))}
    </div>
  );
};

/** Teks dengan stabilo yang tersapu di belakangnya. */
export const Marker: React.FC<{ text: string; delay?: number; duration?: number; size?: number; color?: string; textColor?: string }> = ({
  text,
  delay = 0,
  duration = 12,
  size = 90,
  color,
  textColor,
}) => {
  const t = useTheme();
  const p = useProgress(delay, duration);
  return (
    <span style={{ position: "relative", display: "inline-block", fontWeight: 900, fontSize: size, padding: "0 0.18em", color: textColor ?? t.fg }}>
      <span
        style={{
          position: "absolute",
          left: 0,
          bottom: "0.08em",
          height: "0.5em",
          width: `${p * 100}%`,
          background: color ?? t.accent,
          borderRadius: 8,
          opacity: 0.9,
        }}
      />
      <span style={{ position: "relative" }}>{text}</span>
    </span>
  );
};

/** Angka berjalan, format Indonesia (2.749). */
export const Counter: React.FC<{
  to: number;
  from?: number;
  delay?: number;
  duration?: number;
  decimals?: number;
  prefix?: string;
  suffix?: string;
  size?: number;
  color?: string;
  mono?: boolean;
  style?: React.CSSProperties;
}> = ({ to, from = 0, delay = 0, duration = 40, decimals = 0, prefix = "", suffix = "", size = 200, color, mono = false, style }) => {
  const t = useTheme();
  const p = useProgress(delay, duration);
  const v = from + (to - from) * p;
  const txt = v.toLocaleString("id-ID", { minimumFractionDigits: decimals, maximumFractionDigits: decimals });
  return (
    <span
      style={{
        fontFamily: mono ? MONO : FONT,
        fontWeight: 900,
        fontSize: size,
        lineHeight: 1,
        letterSpacing: "-0.03em",
        fontVariantNumeric: "tabular-nums",
        color: color ?? t.fg,
        ...style,
      }}
    >
      {prefix}
      {txt}
      {suffix}
    </span>
  );
};

/* ============================== Komponen UI ============================== */

/** Kartu bertema dengan entrance pegas. Beri x & y untuk menaruh titik tengahnya di posisi absolut. */
export const Card: React.FC<{
  children?: React.ReactNode;
  delay?: number;
  from?: Dir;
  x?: number;
  y?: number;
  w?: number | string;
  h?: number | string;
  pad?: number;
  radius?: number;
  tilt?: number;
  float?: boolean;
  bg?: string;
  style?: React.CSSProperties;
}> = ({ children, delay = 0, from = "up", x, y, w, h, pad = 44, radius = 40, tilt = 0, float = false, bg, style }) => {
  const t = useTheme();
  const inner = (
    <Pop delay={delay} from={from} float={float}>
      <div
        style={{
          width: w,
          height: h,
          padding: pad,
          borderRadius: radius,
          background: bg ?? t.card,
          border: `2px solid ${t.cardBorder}`,
          boxShadow: t.shadow,
          transform: tilt ? `rotate(${tilt}deg)` : undefined,
          boxSizing: "border-box",
          ...style,
        }}
      >
        {children}
      </div>
    </Pop>
  );
  return x !== undefined && y !== undefined ? <At x={x} y={y}>{inner}</At> : inner;
};

/** Jendela aplikasi (tiga titik + judul mono), cocok untuk tampilan chat/terminal/aplikasi. */
export const Window: React.FC<{ title?: string; children?: React.ReactNode; delay?: number; w?: number; from?: Dir; dark?: boolean }> = ({
  title = "",
  children,
  delay = 0,
  w = 920,
  from = "up",
  dark = true,
}) => (
  <Pop delay={delay} from={from}>
    <div
      style={{
        width: w,
        borderRadius: 32,
        overflow: "hidden",
        background: dark ? "#16151b" : "#ffffff",
        color: dark ? "#ececf1" : "#141414",
        border: `2px solid ${dark ? "rgba(255,255,255,0.1)" : "rgba(0,0,0,0.08)"}`,
        boxShadow: "0 40px 90px rgba(0,0,0,0.45)",
      }}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 14,
          padding: "22px 28px",
          borderBottom: `2px solid ${dark ? "rgba(255,255,255,0.07)" : "rgba(0,0,0,0.06)"}`,
          fontFamily: MONO,
          fontSize: 30,
          opacity: 0.85,
        }}
      >
        {["#ff5f57", "#febc2e", "#28c840"].map((c) => (
          <span key={c} style={{ width: 20, height: 20, borderRadius: 10, background: c, display: "inline-block" }} />
        ))}
        <span style={{ marginLeft: 16 }}>{title}</span>
      </div>
      <div style={{ padding: 40, fontSize: 40, lineHeight: 1.35 }}>{children}</div>
    </div>
  </Pop>
);

/** Label pil kecil, opsional dengan ikon. tone: soft (transparan), solid (warna teks), accent. */
export const Chip: React.FC<{ text: string; icon?: IconName; delay?: number; tone?: "soft" | "solid" | "accent"; size?: number }> = ({
  text,
  icon,
  delay = 0,
  tone = "soft",
  size = 40,
}) => {
  const t = useTheme();
  const bg = tone === "accent" ? t.accent : tone === "solid" ? t.fg : t.card;
  const fg = tone === "accent" ? "#ffffff" : tone === "solid" ? t.bg : t.fg;
  return (
    <Pop delay={delay} from="scale">
      <div
        style={{
          display: "inline-flex",
          alignItems: "center",
          gap: size * 0.35,
          padding: `${size * 0.32}px ${size * 0.7}px`,
          borderRadius: 999,
          background: bg,
          color: fg,
          border: `2px solid ${t.cardBorder}`,
          fontWeight: 800,
          fontSize: size,
          whiteSpace: "nowrap",
          boxShadow: t.shadow,
        }}
      >
        {icon ? <Icon name={icon} size={size * 1.05} color={fg} /> : null}
        {text}
      </div>
    </Pop>
  );
};

/** Progress bar yang terisi sampai `value` (0..1). */
export const Bar: React.FC<{
  value: number;
  delay?: number;
  duration?: number;
  width?: number;
  height?: number;
  color?: string;
  label?: string;
  showPercent?: boolean;
}> = ({ value, delay = 0, duration = 40, width = 900, height = 34, color, label, showPercent = false }) => {
  const t = useTheme();
  const p = useProgress(delay, duration);
  const v = Math.max(0, Math.min(1, value)) * p;
  return (
    <div style={{ width }}>
      {label || showPercent ? (
        <div style={{ display: "flex", justifyContent: "space-between", fontSize: 36, fontWeight: 700, color: t.muted, marginBottom: 14 }}>
          <span>{label ?? ""}</span>
          {showPercent ? <span style={{ fontFamily: MONO, color: t.fg }}>{Math.round(v * 100)}%</span> : null}
        </div>
      ) : null}
      <div style={{ width, height, borderRadius: height, background: t.grid.replace(/[\d.]+\)$/, "0.14)"), overflow: "hidden" }}>
        <div style={{ width: `${v * 100}%`, height: "100%", borderRadius: height, background: color ?? t.accent }} />
      </div>
    </div>
  );
};

/** Kartu statistik: ikon berwarna di atas, nilai besar, label kecil (seperti kartu spesifikasi). */
export const StatCard: React.FC<{
  value: React.ReactNode;
  label: string;
  icon?: IconName;
  iconBg?: string;
  delay?: number;
  w?: number;
  h?: number;
}> = ({ value, label, icon, iconBg, delay = 0, w = 300, h = 380 }) => {
  const t = useTheme();
  return (
    <Card delay={delay} w={w} h={h} pad={32} style={{ display: "flex", flexDirection: "column", justifyContent: "space-between" }}>
      {icon ? <IconBadge name={icon} bg={iconBg ?? t.accent} size={100} /> : <div />}
      <div>
        <div style={{ fontWeight: 900, fontSize: 64, lineHeight: 1.02, letterSpacing: "-0.02em" }}>{value}</div>
        <div style={{ fontFamily: MONO, fontSize: 28, color: t.muted, marginTop: 8 }}>{label}</div>
      </div>
    </Card>
  );
};

/** Mengayun pelan naik-turun. */
export const Float: React.FC<{ children?: React.ReactNode; amp?: number; speed?: number; seed?: string }> = ({ children, amp = 10, speed = 1, seed = "f" }) => {
  const f = useCurrentFrame();
  const ph = random(seed) * Math.PI * 2;
  return <div style={{ transform: `translateY(${Math.sin(f / (20 / speed) + ph) * amp}px)` }}>{children}</div>;
};

/** Path SVG yang tergambar bertahap. Pakai di dalam <svg>. */
export const Draw: React.FC<{
  d: string;
  delay?: number;
  duration?: number;
  stroke?: string;
  width?: number;
  fill?: string;
  dash?: boolean;
}> = ({ d, delay = 0, duration = 25, stroke, width = 8, fill = "none", dash = false }) => {
  const t = useTheme();
  const p = useProgress(delay, duration, Easing.inOut(Easing.cubic));
  return (
    <path
      d={d}
      pathLength={1}
      fill={fill}
      stroke={stroke ?? t.accent}
      strokeWidth={width}
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeDasharray={dash ? "0.02 0.02" : "1 1"}
      strokeDashoffset={dash ? 0 : 1 - p}
      opacity={dash ? p : 1}
    />
  );
};

/* ============================== Ikon ============================== */

type IconDef = { d?: string[]; c?: [number, number, number][]; r?: [number, number, number, number, number][]; solid?: boolean };
const ICONS = {
  check: { d: ["M5 12.5l4.5 4.5L19 7.5"] },
  x: { d: ["M6 6l12 12", "M18 6L6 18"] },
  clock: { c: [[12, 12, 9]], d: ["M12 7v5l3.5 2"] },
  bolt: { d: ["M13 2L4 14h7l-1 8 9-12h-7l1-8z"] },
  fire: { d: ["M12 22c4 0 7-3 7-7 0-4.5-3-6.5-4.5-10.5C12.5 6.5 11.5 8.5 11.5 10.5 10.5 9.5 9.5 8.5 9.5 6.5 6.5 8.5 5 11.5 5 15c0 4 3 7 7 7z"] },
  snow: { d: ["M12 2v20", "M3.5 7l17 10", "M20.5 7l-17 10", "M9 4l3 2 3-2", "M9 20l3-2 3 2"] },
  arrowRight: { d: ["M4 12h16", "M14 6l6 6-6 6"] },
  arrowUp: { d: ["M12 20V4", "M6 10l6-6 6 6"] },
  arrowDown: { d: ["M12 4v16", "M6 14l6 6 6-6"] },
  lock: { r: [[5, 11, 14, 10, 2]], d: ["M8 11V8a4 4 0 018 0v3"] },
  bulb: { d: ["M9 18h6", "M10 21h4", "M12 3a6 6 0 00-4 10.5c.8.8 1 1.5 1 2.5h6c0-1 .2-1.7 1-2.5A6 6 0 0012 3z"] },
  laptop: { r: [[4, 5, 16, 11, 2]], d: ["M2 19h20"] },
  phone: { r: [[7, 2, 10, 20, 2]], d: ["M11 18h2"] },
  heart: { d: ["M12 20.5s-8-4.8-8-10.5a4.5 4.5 0 018-2.8 4.5 4.5 0 018 2.8c0 5.7-8 10.5-8 10.5z"] },
  star: { d: ["M12 3l2.7 5.6 6.1.9-4.4 4.3 1 6.1L12 17l-5.4 2.9 1-6.1-4.4-4.3 6.1-.9z"] },
  warning: { d: ["M12 3l10 18H2L12 3z", "M12 10v4.5", "M12 17.5v.01"] },
  question: { c: [[12, 12, 9]], d: ["M9.5 9.2a2.6 2.6 0 015 .6c0 1.7-2.5 2-2.5 3.7", "M12 16.8v.01"] },
  play: { d: ["M7 4l13 8-13 8z"] },
  eye: { d: ["M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12z"], c: [[12, 12, 3]] },
  drop: { d: ["M12 3s7 7 7 12a7 7 0 01-14 0c0-5 7-12 7-12z"] },
  sun: { c: [[12, 12, 4]], d: ["M12 2v2", "M12 20v2", "M2 12h2", "M20 12h2", "M4.9 4.9l1.4 1.4", "M17.7 17.7l1.4 1.4", "M4.9 19.1l1.4-1.4", "M17.7 6.3l1.4-1.4"] },
  chart: { d: ["M4 20V11", "M10 20V5", "M16 20v-7", "M22 20H2"] },
  users: { c: [[9, 8, 3.5]], d: ["M2 20c0-3.5 3-6 7-6s7 2.5 7 6", "M16 4.5a3.5 3.5 0 010 7", "M18 14c2.5.5 4 2.5 4 6"] },
  money: { r: [[2, 6, 20, 12, 2]], c: [[12, 12, 3]] },
  plus: { d: ["M12 5v14", "M5 12h14"] },
  minus: { d: ["M5 12h14"] },
  search: { c: [[11, 11, 7]], d: ["M16.5 16.5L21 21"] },
  gear: { c: [[12, 12, 3.2]], d: ["M12 2v3", "M12 19v3", "M2 12h3", "M19 12h3", "M4.9 4.9l2.1 2.1", "M17 17l2.1 2.1", "M4.9 19.1L7 17", "M17 7l2.1-2.1"] },
  globe: { c: [[12, 12, 9]], d: ["M3 12h18", "M12 3c3 3.5 3 14.5 0 18", "M12 3c-3 3.5-3 14.5 0 18"] },
  rocket: { d: ["M12 2c4 3 5 8 4 13H8C7 10 8 5 12 2z", "M8 15l-3 4h4", "M16 15l3 4h-4"], c: [[12, 9, 1.8]] },
  sparkle: { d: ["M12 3l2 7 7 2-7 2-2 7-2-7-7-2 7-2z"] },
} satisfies Record<string, IconDef>;
export type IconName = keyof typeof ICONS;

/** Ikon garis sederhana (viewBox 24). */
export const Icon: React.FC<{ name: IconName; size?: number; color?: string; stroke?: number }> = ({ name, size = 64, color, stroke = 2.2 }) => {
  const t = useTheme();
  const def: IconDef = ICONS[name];
  const c = color ?? t.fg;
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke={c} strokeWidth={stroke} strokeLinecap="round" strokeLinejoin="round" style={{ display: "block" }}>
      {def.r?.map(([x, y, w, h, rx], i) => <rect key={`r${i}`} x={x} y={y} width={w} height={h} rx={rx} />)}
      {def.c?.map(([cx, cy, r], i) => <circle key={`c${i}`} cx={cx} cy={cy} r={r} />)}
      {def.d?.map((d, i) => <path key={`d${i}`} d={d} />)}
    </svg>
  );
};

/** Ikon putih di kotak membulat berwarna. */
export const IconBadge: React.FC<{ name: IconName; size?: number; bg?: string; color?: string; delay?: number }> = ({
  name,
  size = 140,
  bg,
  color = "#ffffff",
  delay,
}) => {
  const t = useTheme();
  const box = (
    <div
      style={{
        width: size,
        height: size,
        borderRadius: size * 0.28,
        background: bg ?? t.accent,
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        boxShadow: "0 16px 40px rgba(0,0,0,0.25)",
      }}
    >
      <Icon name={name} size={size * 0.56} color={color} stroke={2.4} />
    </div>
  );
  return delay === undefined ? box : <Pop delay={delay} from="scale">{box}</Pop>;
};
