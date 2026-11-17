import {
  AbsoluteFill,
  Audio,
  Easing,
  Series,
  interpolate,
  spring,
  staticFile,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";
import type { JobData, SceneComponent, SceneWord } from "./types";

const CAPTION_ACCENT = "#ffd23f";
const BRAND = "#ff5a36";

// Job lama tanpa timing kata: bagi durasi secara proporsional terhadap panjang kata.
const estimateWords = (text: string, durationInFrames: number): SceneWord[] => {
  const words = text.trim().split(/\s+/).filter(Boolean);
  const total = words.reduce((s, w) => s + w.length + 1, 0) || 1;
  const speech = Math.max(1, durationInFrames - 15);
  let t = 0;
  return words.map((w) => {
    const len = ((w.length + 1) / total) * speech;
    const out = { t: w, s: Math.round(t), e: Math.round(t + len) };
    t += len;
    return out;
  });
};

// Kelompokkan kata jadi "halaman" subtitle pendek (maks 4 kata, pecah di jeda bicara).
const paginate = (words: SceneWord[], fps: number, maxPerPage = 4): SceneWord[][] => {
  const pages: SceneWord[][] = [];
  let cur: SceneWord[] = [];
  for (let i = 0; i < words.length; i++) {
    const w = words[i];
    const prev = cur[cur.length - 1];
    const gap = prev ? (w.s - prev.e) / fps : 0;
    if (cur.length && (cur.length >= maxPerPage || gap > 0.22 || /[.,!?;:]$/.test(prev?.t ?? ""))) {
      pages.push(cur);
      cur = [];
    }
    cur.push(w);
  }
  if (cur.length) pages.push(cur);
  return pages;
};

const OUTLINE = [
  "0 6px 0 rgba(0,0,0,0.9)",
  "4px 4px 0 #000", "-4px 4px 0 #000", "4px -4px 0 #000", "-4px -4px 0 #000",
  "0 5px 0 #000", "0 -5px 0 #000", "5px 0 0 #000", "-5px 0 0 #000",
  "0 10px 30px rgba(0,0,0,0.55)",
].join(", ");

const Captions: React.FC<{ words: SceneWord[] }> = ({ words }) => {
  const frame = useCurrentFrame();
  const { fps, width, height } = useVideoConfig();
  const landscape = width > height;
  const pages = paginate(words, fps, landscape ? 6 : 4);
  let pi = -1;
  for (let i = 0; i < pages.length; i++) if (frame >= pages[i][0].s - 2) pi = i;
  if (pi < 0) return null;
  const page = pages[pi];
  const enter = spring({ frame: frame - (page[0].s - 2), fps, config: { damping: 12, stiffness: 220 } });
  return (
    <AbsoluteFill style={{ justifyContent: "flex-start", alignItems: "center" }}>
      <div
        style={{
          position: "absolute",
          top: landscape ? height - 205 : height - 415,
          width: landscape ? 1500 : 980,
          textAlign: "center",
          fontFamily: '"Segoe UI", Arial, sans-serif',
          fontWeight: 900,
          fontSize: landscape ? 64 : 78,
          lineHeight: 1.12,
          letterSpacing: "-0.01em",
          transform: `scale(${0.85 + 0.15 * enter})`,
          opacity: Math.min(1, enter * 2),
        }}
      >
        {page.map((w, i) => {
          const active = frame >= w.s && frame < Math.max(w.e, w.s + 4);
          const spoken = frame >= w.s;
          const pop = spring({ frame: frame - w.s, fps, config: { damping: 10, stiffness: 300 } });
          return (
            <span
              key={i}
              style={{
                display: "inline-block",
                margin: "0 0.2em",
                color: active ? CAPTION_ACCENT : spoken ? "#ffffff" : "rgba(255,255,255,0.72)",
                transform: `scale(${active ? 1 + 0.08 * pop : 1})`,
                textShadow: OUTLINE,
              }}
            >
              {w.t}
            </span>
          );
        })}
      </div>
    </AbsoluteFill>
  );
};

// Transisi masuk scene: punch zoom + blur singkat.
const SceneIn: React.FC<{ first: boolean; children: React.ReactNode }> = ({ first, children }) => {
  const frame = useCurrentFrame();
  if (first) return <AbsoluteFill>{children}</AbsoluteFill>;
  const p = interpolate(frame, [0, 9], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp", easing: Easing.out(Easing.cubic) });
  const flash = interpolate(frame, [0, 6], [0.35, 0], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  return (
    <AbsoluteFill>
      <AbsoluteFill style={{ transform: `scale(${1.15 - 0.15 * p})`, filter: p < 1 ? `blur(${(1 - p) * 16}px)` : undefined }}>
        {children}
      </AbsoluteFill>
      <AbsoluteFill style={{ background: "#ffffff", opacity: flash }} />
    </AbsoluteFill>
  );
};

const ProgressBar: React.FC<{ total: number }> = ({ total }) => {
  const frame = useCurrentFrame();
  return (
    <div style={{ position: "absolute", left: 0, top: 0, height: 10, width: `${(frame / Math.max(1, total)) * 100}%`, background: BRAND }} />
  );
};

export const AutoVideo: React.FC<{ job: JobData; scenes: SceneComponent[] }> = ({ job, scenes }) => {
  const total = job.scenes.reduce((s, sc) => s + sc.durationInFrames, 0);
  return (
    <AbsoluteFill style={{ backgroundColor: "#0c0c10" }}>
      {job.music ? <Audio src={staticFile(job.music)} volume={0.08} loop /> : null}
      <Series>
        {job.scenes.map((s, i) => {
          const Scene = scenes[i];
          const words = s.words?.length ? s.words : estimateWords(s.narasi, s.durationInFrames);
          return (
            <Series.Sequence key={i} durationInFrames={s.durationInFrames}>
              <SceneIn first={i === 0}>{Scene ? <Scene durationInFrames={s.durationInFrames} words={words} /> : null}</SceneIn>
              {i > 0 && job.sfx ? <Audio src={staticFile("sfx/whoosh.wav")} volume={0.35} /> : null}
              {s.audio ? <Audio src={staticFile(s.audio)} /> : null}
              <Captions words={words} />
            </Series.Sequence>
          );
        })}
      </Series>
      <ProgressBar total={total} />
    </AbsoluteFill>
  );
};
