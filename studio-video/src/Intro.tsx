import {
  AbsoluteFill,
  interpolate,
  spring,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";

type Props = { title: string; subtitle: string };

export const Intro: React.FC<Props> = ({ title, subtitle }) => {
  const frame = useCurrentFrame();
  const { fps, durationInFrames } = useVideoConfig();

  // Judul muncul dengan efek pegas
  const scale = spring({ frame, fps, config: { damping: 12 } });

  // Subjudul fade-in & naik setelah 20 frame
  const subOpacity = interpolate(frame, [20, 40], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  const subY = interpolate(frame, [20, 40], [30, 0], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });

  // Fade-out di 15 frame terakhir
  const fadeOut = interpolate(frame, [durationInFrames - 15, durationInFrames], [1, 0], { extrapolateLeft: "clamp" });

  // Lingkaran latar yang berputar
  const rotation = frame * 2;

  return (
    <AbsoluteFill style={{ background: "linear-gradient(135deg, #0f172a, #312e81)", justifyContent: "center", alignItems: "center", opacity: fadeOut, fontFamily: "Segoe UI, sans-serif" }}>
      <div style={{ position: "absolute", width: 700, height: 700, borderRadius: "50%", border: "12px dashed rgba(129,140,248,0.35)", transform: `rotate(${rotation}deg)` }} />
      <h1 style={{ color: "white", fontSize: 140, margin: 0, transform: `scale(${scale})` }}>{title}</h1>
      <p style={{ color: "#a5b4fc", fontSize: 56, marginTop: 24, opacity: subOpacity, transform: `translateY(${subY}px)` }}>{subtitle}</p>
    </AbsoluteFill>
  );
};
