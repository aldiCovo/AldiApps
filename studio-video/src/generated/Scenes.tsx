import React from "react";
import { AbsoluteFill, Easing, interpolate, useCurrentFrame } from "remotion";
import {
  Shots,
  Safe,
  Kinetic,
  Counter,
  Card,
  Chip,
  Bar,
  IconBadge,
  Icon,
  Pop,
  Float,
  Sfx,
  Draw,
  useTheme,
  useEnter,
  useProgress,
  FONT,
  wordAt,
} from "../auto/kit";
import type { SceneComponent } from "../auto/types";

/* ------------------------------------------------------------------ */
/* constants & helpers                                                 */
/* ------------------------------------------------------------------ */

const CL = { extrapolateLeft: "clamp", extrapolateRight: "clamp" } as const;
const EASE = Easing.bezier(0.22, 1, 0.36, 1);

const ORANGE = "#ff5a36";
const MINT = "#33d69f";
const DEEP = "#0d2b20";
const RED = "#ef3b3b";
const INK = "#16130f";
const CREAM = "#fff4e2";
const GINGER = "#ff9a3d";
const BOX = "#d89a5b";
const BOX_D = "#b97b3f";
const BOX_L = "#e9b77c";
const BOX_IN = "#7a4a22";
const BOX_LINE = "#5a3415";
const TOWER = "#c98848";

const cl = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

const T: React.FC<{
  children: React.ReactNode;
  size?: number;
  weight?: number;
  color?: string;
  muted?: boolean;
  style?: React.CSSProperties;
}> = ({ children, size = 44, weight = 800, color, muted, style }) => {
  const th = useTheme();
  return (
    <div
      style={{
        fontFamily: FONT,
        fontSize: size,
        fontWeight: weight,
        color: color ?? (muted ? th.muted : th.fg),
        lineHeight: 1.1,
        textAlign: "center",
        ...style,
      }}
    >
      {children}
    </div>
  );
};

const Punch: React.FC<{ at: number; amount?: number; children: React.ReactNode }> = ({
  at,
  amount = 0.16,
  children,
}) => {
  const f = useCurrentFrame();
  const a = Math.max(0, at);
  const b = interpolate(f, [a, a + 4, a + 14], [0, 1, 0], CL);
  return <div style={{ transform: `scale(${1 + amount * b})` }}>{children}</div>;
};

/* ------------------------------------------------------------------ */
/* illustrations                                                       */
/* ------------------------------------------------------------------ */

type Eyes = "open" | "closed" | "happy";

const CatPaths: React.FC<{ color?: string; eyes?: Eyes; tail?: boolean }> = ({
  color = GINGER,
  eyes = "open",
  tail = true,
}) => {
  const dark = "#1a1410";
  const line = color === INK ? "#ffe9c7" : dark;
  return (
    <g>
      {tail ? (
        <path
          d="M146 172 Q200 166 186 110"
          fill="none"
          stroke={color}
          strokeWidth={16}
          strokeLinecap="round"
        />
      ) : null}
      <ellipse cx={100} cy={146} rx={56} ry={50} fill={color} />
      <ellipse cx={100} cy={154} rx={28} ry={30} fill="#ffffff" opacity={0.16} />
      <polygon points="60,60 66,6 100,40" fill={color} />
      <polygon points="140,60 134,6 100,40" fill={color} />
      <circle cx={100} cy={76} r={44} fill={color} />
      <polygon points="70,46 72,22 88,38" fill="#ff9fb2" />
      <polygon points="130,46 128,22 112,38" fill="#ff9fb2" />
      {eyes === "open" ? (
        <>
          <ellipse cx={83} cy={76} rx={9} ry={12} fill="#fff3c4" />
          <ellipse cx={117} cy={76} rx={9} ry={12} fill="#fff3c4" />
          <ellipse cx={84} cy={77} rx={4} ry={9} fill={dark} />
          <ellipse cx={118} cy={77} rx={4} ry={9} fill={dark} />
        </>
      ) : (
        <>
          <path
            d={eyes === "closed" ? "M73 78 Q83 87 93 78" : "M73 82 Q83 70 93 82"}
            fill="none"
            stroke={line}
            strokeWidth={5}
            strokeLinecap="round"
          />
          <path
            d={eyes === "closed" ? "M107 78 Q117 87 127 78" : "M107 82 Q117 70 127 82"}
            fill="none"
            stroke={line}
            strokeWidth={5}
            strokeLinecap="round"
          />
        </>
      )}
      <path d="M94 92 L106 92 L100 99 Z" fill="#ff7f99" />
      <path
        d="M100 99 Q94 106 88 103 M100 99 Q106 106 112 103"
        fill="none"
        stroke={line}
        strokeWidth={3}
        strokeLinecap="round"
      />
      <g stroke="#ffffff" strokeOpacity={0.7} strokeWidth={3} strokeLinecap="round">
        <line x1={70} y1={95} x2={34} y2={88} />
        <line x1={70} y1={102} x2={34} y2={106} />
        <line x1={130} y1={95} x2={166} y2={88} />
        <line x1={130} y1={102} x2={166} y2={106} />
      </g>
    </g>
  );
};

const BoxWithCat: React.FC<{
  w: number;
  rise: number;
  eyes?: Eyes;
  catColor?: string;
  squash?: number;
}> = ({ w, rise, eyes = "open", catColor = GINGER, squash = 1 }) => {
  const r = cl(rise, 0, 1.1);
  const ty = 155 - 115 * r;
  return (
    <svg width={w} height={(w * 340) / 400} viewBox="0 0 400 340" style={{ overflow: "visible" }}>
      <g transform={`translate(200 332) scale(${2 - squash} ${squash}) translate(-200 -332)`}>
        <polygon
          points="95,116 110,58 290,58 305,116"
          fill={BOX_D}
          stroke={BOX_LINE}
          strokeWidth={4}
          strokeLinejoin="round"
        />
        <polygon
          points="50,150 95,116 305,116 350,150"
          fill={BOX_IN}
          stroke={BOX_LINE}
          strokeWidth={4}
          strokeLinejoin="round"
        />
        {r > 0.02 ? (
          <g transform={`translate(110 ${ty}) scale(0.9)`}>
            <CatPaths color={catColor} eyes={eyes} />
          </g>
        ) : null}
        <rect x={50} y={150} width={300} height={182} fill={BOX} stroke={BOX_LINE} strokeWidth={4} />
        <polygon
          points="50,150 95,116 38,72 4,104"
          fill={BOX_L}
          stroke={BOX_LINE}
          strokeWidth={4}
          strokeLinejoin="round"
        />
        <polygon
          points="350,150 305,116 362,72 396,104"
          fill={BOX_L}
          stroke={BOX_LINE}
          strokeWidth={4}
          strokeLinejoin="round"
        />
        <rect x={182} y={150} width={36} height={64} fill="#f2cf98" opacity={0.85} />
        <path d="M80 296 h56 M80 312 h36" stroke={BOX_D} strokeWidth={6} strokeLinecap="round" />
      </g>
    </svg>
  );
};

const LuxBed: React.FC<{ w: number }> = ({ w }) => (
  <svg width={w} height={(w * 250) / 460} viewBox="0 0 460 250">
    <ellipse cx={230} cy={236} rx={190} ry={12} fill="rgba(0,0,0,0.14)" />
    <rect x={80} y={190} width={18} height={42} rx={6} fill="#e8b84a" />
    <rect x={362} y={190} width={18} height={42} rx={6} fill="#e8b84a" />
    <path d="M28 140 Q28 214 230 214 Q432 214 432 140 Z" fill="#5b3fd6" />
    <ellipse cx={230} cy={140} rx={202} ry={64} fill="#7f62ff" />
    <ellipse cx={230} cy={142} rx={160} ry={44} fill="#efe7ff" />
    <ellipse cx={230} cy={150} rx={124} ry={28} fill="#ffffff" />
    {[170, 230, 290].map((x) => (
      <circle key={x} cx={x} cy={150} r={5} fill="#d9ccff" />
    ))}
    <path d="M200 40 L212 70 H248 L260 40 L242 54 L230 30 L218 54 Z" fill="#ffc531" />
  </svg>
);

const PawSvg: React.FC<{ size: number; color: string }> = ({ size, color }) => (
  <svg width={size} height={size} viewBox="0 0 100 100">
    <ellipse cx={50} cy={66} rx={23} ry={19} fill={color} />
    <ellipse cx={24} cy={42} rx={9} ry={11} fill={color} />
    <ellipse cx={41} cy={27} rx={9} ry={11} fill={color} />
    <ellipse cx={59} cy={27} rx={9} ry={11} fill={color} />
    <ellipse cx={76} cy={42} rx={9} ry={11} fill={color} />
  </svg>
);

const PawNum: React.FC<{ n: number; delay?: number; size?: number }> = ({
  n,
  delay = 0,
  size = 150,
}) => {
  const e = useEnter(delay);
  const th = useTheme();
  return (
    <div
      style={{
        width: size,
        height: size,
        borderRadius: size * 0.28,
        background: BOX,
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        position: "relative",
        transform: `scale(${Math.max(0, e)}) rotate(${(1 - e) * -30}deg)`,
        boxShadow: "0 14px 30px rgba(0,0,0,0.22)",
      }}
    >
      <PawSvg size={size * 0.62} color={INK} />
      <div
        style={{
          position: "absolute",
          right: -16,
          top: -16,
          width: size * 0.44,
          height: size * 0.44,
          borderRadius: "50%",
          background: ORANGE,
          color: "#fff",
          fontFamily: FONT,
          fontWeight: 900,
          fontSize: size * 0.28,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          border: `6px solid ${th.bg}`,
        }}
      >
        {n}
      </div>
    </div>
  );
};

const ShieldBadge: React.FC<{ size?: number; delay?: number }> = ({ size = 150, delay = 0 }) => {
  const e = useEnter(delay);
  return (
    <div
      style={{
        width: size,
        height: size,
        borderRadius: size * 0.28,
        background: ORANGE,
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        transform: `scale(${Math.max(0, e)}) rotate(${(1 - e) * 25}deg)`,
        boxShadow: "0 14px 30px rgba(0,0,0,0.25)",
      }}
    >
      <svg width={size * 0.62} height={size * 0.62} viewBox="0 0 100 100">
        <path d="M50 8 L86 21 V47 C86 70 70 85 50 93 C30 85 14 70 14 47 V21 Z" fill="#fff" />
        <path
          d="M34 50 L46 62 L68 38"
          fill="none"
          stroke={ORANGE}
          strokeWidth={9}
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </div>
  );
};

const DashBadge: React.FC<{ size?: number; delay?: number }> = ({ size = 150, delay = 0 }) => {
  const e = useEnter(delay);
  return (
    <div
      style={{
        width: size,
        height: size,
        borderRadius: size * 0.28,
        background: ORANGE,
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        transform: `scale(${Math.max(0, e)}) rotate(${(1 - e) * -25}deg)`,
        boxShadow: "0 14px 30px rgba(0,0,0,0.25)",
      }}
    >
      <svg width={size * 0.6} height={size * 0.6} viewBox="0 0 100 100">
        <rect
          x={14}
          y={14}
          width={72}
          height={72}
          rx={6}
          fill="none"
          stroke="#fff"
          strokeWidth={8}
          strokeDasharray="14 10"
        />
      </svg>
    </div>
  );
};

const FoldFrame: React.FC = () => {
  const f = useCurrentFrame();
  const op = interpolate(f, [0, 26, 44], [1, 1, 0.22], CL);
  const fold = interpolate(f, [0, 12], [0, 1], { ...CL, easing: EASE });
  const corners: [number, number, number, number][] = [
    [90, 170, 1, 1],
    [990, 170, -1, 1],
    [990, 1490, -1, -1],
    [90, 1490, 1, -1],
  ];
  return (
    <AbsoluteFill style={{ pointerEvents: "none", opacity: op }}>
      <svg width="100%" height="100%" viewBox="0 0 1080 1920">
        <Draw d="M90 170 H990 V1490 H90 Z" delay={0} duration={16} stroke={BOX} width={12} />
        {corners.map(([x, y, sx, sy], i) => (
          <polygon
            key={i}
            points={`${x},${y} ${x + sx * 90 * fold},${y} ${x},${y + sy * 90 * fold}`}
            fill={BOX}
          />
        ))}
      </svg>
    </AbsoluteFill>
  );
};

/* ------------------------------------------------------------------ */
/* Scene 1                                                             */
/* ------------------------------------------------------------------ */

const S1Product: React.FC = () => (
  <>
    <Safe gap={46}>
      <Kinetic text={"KASUR\n*MAHAL*"} size={150} delay={0} stagger={8} />
      <Float>
        <Card delay={4} w={800} pad={44}>
          <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 22 }}>
            <LuxBed w={600} />
            <T size={50}>Kasur Kucing Premium</T>
            <div style={{ display: "flex", gap: 8 }}>
              {[0, 1, 2, 3, 4].map((i) => (
                <Pop key={i} delay={8 + i * 2} from="scale">
                  <Icon name="star" size={44} color="#ffb400" />
                </Pop>
              ))}
            </div>
            <Chip text="Rp 500.000" icon="money" tone="accent" delay={16} />
          </div>
        </Card>
      </Float>
    </Safe>
    <Sfx name="pop" at={16} volume={0.45} />
  </>
);

const CatJump: React.FC<{ land: number }> = ({ land }) => {
  const f = useCurrentFrame();
  const L = Math.max(land, 14);
  const p = interpolate(f, [3, L], [0, 1], { ...CL, easing: Easing.inOut(Easing.quad) });
  const pan = interpolate(f, [0, 10], [260, 0], { ...CL, easing: EASE });
  const flying = f < L;
  const cx = interpolate(p, [0, 1], [130, 705]);
  const cy = interpolate(p, [0, 1], [190, 130]) - Math.sin(p * Math.PI) * 240;
  const rot = interpolate(p, [0, 1], [-18, 22]);
  const rise = interpolate(f, [L, L + 7, L + 14], [0.2, 1, 0.82], CL);
  const squash = interpolate(f, [L, L + 4, L + 9, L + 15], [1, 0.8, 1.08, 1], CL);
  const trail = p > 0.02 && p < 0.98 ? 1 : 0;
  return (
    <div style={{ position: "relative", width: 1000, height: 420, transform: `translateX(${pan}px)` }}>
      <div style={{ position: "absolute", left: 30, top: 214 }}>
        <LuxBed w={380} />
      </div>
      <div style={{ position: "absolute", left: 620, top: 120 }}>
        <BoxWithCat w={330} rise={flying ? 0 : rise} eyes="happy" catColor={INK} squash={squash} />
      </div>
      {flying ? (
        <svg
          style={{ position: "absolute", left: 0, top: 0, overflow: "visible" }}
          width={1000}
          height={420}
          viewBox="0 0 1000 420"
        >
          {[0, 1, 2].map((i) => (
            <line
              key={i}
              x1={cx - 10 - i * 10}
              y1={cy + 50 + i * 30}
              x2={cx - 120 - i * 30}
              y2={cy + 70 + i * 30}
              stroke={INK}
              strokeOpacity={0.35 * trail}
              strokeWidth={8}
              strokeLinecap="round"
            />
          ))}
          <g transform={`translate(${cx} ${cy}) rotate(${rot} 80 80) scale(0.8)`}>
            <CatPaths color={INK} eyes="open" />
          </g>
        </svg>
      ) : null}
    </div>
  );
};

const VsCard: React.FC<{ ok: boolean; title: string; price: string; delay: number }> = ({
  ok,
  title,
  price,
  delay,
}) => (
  <Card delay={delay} w={440} pad={30}>
    <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 12 }}>
      <IconBadge name={ok ? "check" : "x"} size={96} bg={ok ? MINT : RED} delay={delay + 6} />
      <T size={38} weight={700} muted>
        {title}
      </T>
      <T size={56} weight={900}>
        {price}
      </T>
    </div>
  </Card>
);

const S1Win: React.FC<{ land: number }> = ({ land }) => (
  <>
    <Safe gap={34}>
      <Kinetic text={"KARDUS\n*MENANG*"} size={130} delay={land + 2} stagger={6} />
      <CatJump land={land} />
      <div style={{ display: "flex", gap: 36 }}>
        <VsCard ok={false} title="Kasur" price="Rp 500 ribu" delay={land + 12} />
        <VsCard ok title="Kardus" price="Rp 0" delay={land + 18} />
      </div>
    </Safe>
    <Sfx name="pop" at={land} volume={0.5} />
  </>
);

/* ------------------------------------------------------------------ */
/* Scene 2                                                             */
/* ------------------------------------------------------------------ */

const ProtectBox: React.FC = () => {
  const th = useTheme();
  const f = useCurrentFrame();
  const rise = interpolate(f, [8, 22], [0.25, 0.8], { ...CL, easing: EASE });
  const bob = f > 22 ? Math.sin(f / 8) * 0.03 : 0;
  return (
    <div style={{ position: "relative", width: 900, height: 440 }}>
      <svg
        width={900}
        height={440}
        viewBox="0 0 900 440"
        style={{ position: "absolute", left: 0, top: 0, overflow: "visible" }}
      >
        <Draw
          d="M150 430 C150 150 250 50 350 50 C450 50 550 150 550 430"
          delay={16}
          duration={22}
          stroke={th.accent}
          width={7}
        />
      </svg>
      <div style={{ position: "absolute", left: 160, top: 105 }}>
        <BoxWithCat w={380} rise={rise + bob} eyes="open" />
      </div>
      <svg
        width={900}
        height={440}
        viewBox="0 0 900 440"
        style={{ position: "absolute", left: 0, top: 0, overflow: "visible" }}
      >
        <Draw d="M760 84 Q640 40 480 196" delay={36} duration={16} stroke={th.fg} width={8} />
        <Draw d="M519 188 L480 196 L492 158" delay={50} duration={6} stroke={th.fg} width={8} />
      </svg>
      <div style={{ position: "absolute", right: -10, top: 0 }}>
        <Chip text="punggung terlindungi" icon="lock" tone="accent" delay={44} />
      </div>
    </div>
  );
};

const S2Safe: React.FC<{ aman: number }> = ({ aman }) => (
  <>
    <FoldFrame />
    <Safe gap={36}>
      <div style={{ display: "flex", gap: 34, alignItems: "center" }}>
        <PawNum n={1} delay={4} />
        <ShieldBadge delay={14} />
      </div>
      <Punch at={aman}>
        <Kinetic text={"BENTENG\n*AMAN*"} size={140} delay={Math.max(0, aman - 25)} stagger={22} />
      </Punch>
      <ProtectBox />
    </Safe>
    <Sfx name="pop" at={Math.max(0, aman)} volume={0.35} />
  </>
);

const StudyCard: React.FC<{
  title: string;
  sub: string;
  delay: number;
  chip: string;
  chipDelay: number;
}> = ({ title, sub, delay, chip, chipDelay }) => {
  const e = useEnter(delay);
  const l1 = useProgress(delay + 8, 18);
  const l2 = useProgress(delay + 12, 18);
  const k = cl(e * 1.5, 0, 1);
  return (
    <div
      style={{
        width: 840,
        background: MINT,
        borderRadius: 44,
        padding: "40px 52px",
        fontFamily: FONT,
        color: DEEP,
        boxShadow: "0 30px 70px rgba(0,0,0,0.22)",
        transform: `translateY(${(1 - e) * 140}px) rotate(${(1 - e) * -4}deg)`,
        opacity: k,
        display: "flex",
        flexDirection: "column",
        gap: 18,
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 16 }}>
        <div
          style={{
            width: 70,
            height: 70,
            borderRadius: 20,
            background: DEEP,
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
          }}
        >
          <Icon name="search" size={40} color={MINT} />
        </div>
        <div style={{ fontSize: 34, fontWeight: 800, letterSpacing: 4, opacity: 0.75 }}>
          STUDI ILMIAH
        </div>
      </div>
      <div style={{ fontSize: 96, fontWeight: 900, lineHeight: 1 }}>{title}</div>
      <div style={{ fontSize: 52, fontWeight: 800 }}>– {sub}</div>
      <div style={{ height: 16, width: 620 * l1, background: DEEP, opacity: 0.2, borderRadius: 8 }} />
      <div style={{ height: 16, width: 440 * l2, background: DEEP, opacity: 0.2, borderRadius: 8 }} />
      <div style={{ alignSelf: "flex-start" }}>
        <Chip text={chip} icon="users" tone="solid" delay={chipDelay} />
      </div>
    </div>
  );
};

const S2Study: React.FC<{ ding: number }> = ({ ding }) => (
  <>
    <Safe gap={44}>
      <Kinetic text={"ADA *RISETNYA*"} size={110} delay={0} stagger={5} />
      <Float>
        <StudyCard
          title="STUDI 2014"
          sub="Utrecht"
          delay={Math.max(0, ding - 2)}
          chip="kucing penampungan"
          chipDelay={ding + 22}
        />
      </Float>
      <div style={{ display: "flex", gap: 20 }}>
        <Chip text="Utrecht, Belanda" icon="globe" tone="soft" delay={ding + 28} />
        <Chip text="kotak vs tanpa kotak" icon="search" tone="soft" delay={ding + 34} />
      </div>
    </Safe>
    <Sfx name="pop" at={Math.max(0, ding)} volume={0.45} />
  </>
);

const StressBars: React.FC = () => {
  const th = useTheme();
  const f = useCurrentFrame();
  const a = interpolate(f, [0, 12, 64], [0, 0.92, 0.7], { ...CL, easing: EASE });
  const b = interpolate(f, [0, 12, 36], [0, 0.92, 0.2], { ...CL, easing: EASE });
  const arrowOp = interpolate(f, [18, 26], [0, 1], CL);
  const H = 480;
  const col = (label: string, v: number, color: string, ok: boolean, d: number) => (
    <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 22 }}>
      <Pop delay={d} from="down">
        <T size={44} weight={900}>
          {label}
        </T>
      </Pop>
      <div style={{ position: "relative", width: 200, height: H }}>
        <div
          style={{
            position: "absolute",
            inset: 0,
            borderRadius: 40,
            background: th.card,
            overflow: "hidden",
          }}
        >
          <div
            style={{
              position: "absolute",
              left: 0,
              right: 0,
              bottom: 0,
              height: H * v,
              background: color,
              borderRadius: 40,
            }}
          />
        </div>
        {ok ? (
          <div
            style={{
              position: "absolute",
              right: -96,
              top: H * (1 - v) - 40,
              opacity: arrowOp,
            }}
          >
            <Icon name="arrowDown" size={84} color={MINT} />
          </div>
        ) : null}
      </div>
      <IconBadge name={ok ? "check" : "x"} size={100} bg={ok ? MINT : RED} delay={d + 16} />
    </div>
  );
  return (
    <div style={{ display: "flex", gap: 140, alignItems: "flex-start" }}>
      {col("Tanpa kotak", a, RED, false, 2)}
      {col("Dengan kotak", b, MINT, true, 6)}
    </div>
  );
};

const S2Stress: React.FC = () => (
  <Safe gap={34}>
    <Kinetic text={"STRES *TURUN*"} size={120} delay={0} stagger={6} />
    <T size={36} weight={700} muted>
      tingkat stres kucing
    </T>
    <StressBars />
  </Safe>
);

const SleepyBox: React.FC = () => {
  const f = useCurrentFrame();
  return (
    <div style={{ position: "relative", width: 560, height: 390 }}>
      <div style={{ position: "absolute", left: 70, top: 26 }}>
        <BoxWithCat w={420} rise={0.86 + Math.sin(f / 10) * 0.02} eyes="closed" catColor={INK} />
      </div>
      {[0, 1, 2].map((i) => {
        const t = (f + i * 14) % 42;
        return (
          <div
            key={i}
            style={{
              position: "absolute",
              left: 400 + i * 30 + t * 0.6,
              top: 90 - t * 2 - i * 22,
              fontFamily: FONT,
              fontWeight: 900,
              fontSize: 44 + i * 12,
              color: CREAM,
              opacity: interpolate(t, [0, 8, 34, 42], [0, 1, 1, 0]),
            }}
          >
            z
          </div>
        );
      })}
    </div>
  );
};

const S2Calm: React.FC = () => (
  <Safe gap={40}>
    <Kinetic text={"LEBIH CEPAT\n*TENANG*"} size={120} delay={0} stagger={4} />
    <SleepyBox />
    <Bar value={1} label="adaptasi" delay={8} duration={22} width={760} />
  </Safe>
);

/* ------------------------------------------------------------------ */
/* Scene 3                                                             */
/* ------------------------------------------------------------------ */

const HeatWaves: React.FC = () => {
  const f = useCurrentFrame();
  const th = useTheme();
  const e = useProgress(18, 18);
  return (
    <svg width={760} height={150} viewBox="0 0 760 150" style={{ opacity: e }}>
      {[0, 1, 2].map((i) => {
        const x0 = 160 + i * 220;
        let d = `M${x0} 140`;
        for (let k = 1; k <= 12; k++) {
          const y = 140 - k * 10;
          const x = x0 + Math.sin(k * 0.9 - f / 5 + i) * 16;
          d += ` L${x.toFixed(1)} ${y}`;
        }
        return (
          <path
            key={i}
            d={d}
            fill="none"
            stroke={th.accent}
            strokeWidth={10}
            strokeLinecap="round"
            strokeLinejoin="round"
            opacity={0.85}
          />
        );
      })}
    </svg>
  );
};

const S3Hot: React.FC<{ hangat: number }> = ({ hangat }) => (
  <>
    <FoldFrame />
    <Safe gap={44}>
      <PawNum n={2} delay={4} />
      <Float>
        <IconBadge name="fire" size={200} bg={ORANGE} delay={14} />
      </Float>
      <Punch at={hangat + 2} amount={0.12}>
        <Kinetic text={"*HANGAT*"} size={190} delay={Math.max(0, hangat - 4)} stagger={4} />
      </Punch>
      <HeatWaves />
    </Safe>
    <Sfx name="pop" at={Math.max(0, hangat - 2)} volume={0.35} />
  </>
);

const Thermo: React.FC<{ startAt: number; endAt: number }> = ({ startAt, endAt }) => {
  const th = useTheme();
  const f = useCurrentFrame();
  const s = Math.max(1, startAt);
  const en = Math.max(s + 1, endAt);
  const temp = interpolate(f, [0, s, en], [12, 30, 38], CL);
  const Y = (t: number) => 560 - (t / 45) * 500;
  const zone = useProgress(s, 14);
  return (
    <svg width={240} height={700} viewBox="0 0 240 700">
      <rect
        x={66}
        y={34}
        width={68}
        height={590}
        rx={34}
        fill={th.card}
        stroke={th.fg}
        strokeOpacity={0.15}
        strokeWidth={4}
      />
      <rect
        x={50}
        y={Y(38)}
        width={100}
        height={Y(30) - Y(38)}
        rx={14}
        fill={th.accent}
        fillOpacity={0.22 * zone}
        stroke={th.accent}
        strokeWidth={4}
        strokeDasharray="12 8"
        strokeOpacity={zone}
      />
      <rect x={82} y={Y(temp)} width={36} height={620 - Y(temp)} rx={18} fill={th.accent} />
      <circle cx={100} cy={620} r={62} fill={th.accent} />
      <circle cx={84} cy={604} r={16} fill="#fff" opacity={0.35} />
      {[0, 10, 20, 30, 40].map((t) => (
        <g key={t}>
          <line
            x1={154}
            x2={174}
            y1={Y(t)}
            y2={Y(t)}
            stroke={th.fg}
            strokeWidth={5}
            strokeLinecap="round"
            opacity={0.5}
          />
          <text
            x={182}
            y={Y(t) + 10}
            fontFamily={FONT}
            fontSize={28}
            fontWeight={700}
            fill={th.fg}
            opacity={0.6}
          >
            {t}
          </text>
        </g>
      ))}
    </svg>
  );
};

const S3Thermo: React.FC<{ startAt: number; endAt: number }> = ({ startAt, endAt }) => {
  const s = Math.max(1, startAt);
  const en = Math.max(s + 4, endAt);
  const span = en - s;
  return (
    <>
      <Safe gap={36}>
        <Kinetic text={"SUHU NYAMAN\n*KUCING*"} size={110} delay={0} stagger={4} />
        <div style={{ display: "flex", alignItems: "center", gap: 50 }}>
          <Thermo startAt={s} endAt={en} />
          <div style={{ display: "flex", flexDirection: "column", alignItems: "flex-start", gap: 26 }}>
            <Counter from={30} to={38} delay={s} duration={span} size={190} suffix="°C" />
            <Chip text="zona hangat" icon="fire" tone="accent" delay={s + 6} />
          </div>
        </div>
      </Safe>
      {[0, 0.25, 0.5, 0.75, 1].map((k, i) => (
        <Sfx key={i} name="tick" at={Math.round(s + span * k)} volume={0.25} />
      ))}
    </>
  );
};

const HumanFig: React.FC<{ color: string }> = ({ color }) => (
  <svg width={150} height={210} viewBox="0 0 150 220">
    <circle cx={75} cy={46} r={34} fill={color} />
    <path d="M20 214 V140 Q20 92 75 92 Q130 92 130 140 V214 Z" fill={color} />
  </svg>
);

const MiniThermo: React.FC<{ level: number; color: string; delay: number }> = ({
  level,
  color,
  delay,
}) => {
  const th = useTheme();
  const p = useProgress(delay, 18);
  const h = 160 * level * p;
  return (
    <svg width={56} height={220} viewBox="0 0 56 220">
      <rect
        x={16}
        y={6}
        width={24}
        height={180}
        rx={12}
        fill="none"
        stroke={th.fg}
        strokeOpacity={0.3}
        strokeWidth={3}
      />
      <rect x={21} y={180 - h} width={14} height={h + 10} rx={7} fill={color} />
      <circle cx={28} cy={194} r={22} fill={color} />
    </svg>
  );
};

const S3Compare: React.FC = () => {
  const th = useTheme();
  return (
    <Safe gap={40}>
      <Kinetic text={"BEDA *SUHU*"} size={110} delay={0} stagger={5} />
      <div style={{ display: "flex", gap: 30 }}>
        <Card delay={2} w={450} pad={32}>
          <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 12 }}>
            <div style={{ display: "flex", alignItems: "flex-end", gap: 16 }}>
              <HumanFig color={th.muted} />
              <MiniThermo level={0.47} color="#5b9dff" delay={6} />
            </div>
            <T size={38} weight={700} muted>
              manusia
            </T>
            <T size={78} weight={900}>
              ±21°C
            </T>
          </div>
        </Card>
        <Card delay={8} w={450} pad={32}>
          <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 12 }}>
            <div style={{ display: "flex", alignItems: "flex-end", gap: 16 }}>
              <svg width={200} height={210} viewBox="0 0 200 205">
                <CatPaths color={GINGER} eyes="happy" />
              </svg>
              <MiniThermo level={0.8} color={ORANGE} delay={12} />
            </div>
            <T size={38} weight={700} muted>
              kucing
            </T>
            <T size={70} weight={900} color={th.accent}>
              30–38°C
            </T>
          </div>
        </Card>
      </div>
      <Chip text="lebih hangat" icon="arrowUp" tone="accent" delay={18} />
    </Safe>
  );
};

const HeatBox: React.FC = () => {
  const f = useCurrentFrame();
  const e = useEnter(0);
  const glow = 0.3 + 0.18 * Math.sin(f / 6);
  const sc = Math.min(1.04, Math.max(0, e));
  const loops: { d: string; head: string; delay: number }[] = [
    {
      d: "M355 410 C280 400 200 330 215 270 C230 220 300 250 330 330",
      head: "M336 296 L330 330 L304 308",
      delay: 12,
    },
    {
      d: "M545 410 C620 400 700 330 685 270 C670 220 600 250 570 330",
      head: "M564 296 L570 330 L596 308",
      delay: 18,
    },
    {
      d: "M450 335 C450 280 420 230 450 225 C490 220 500 270 480 310",
      head: "M505 294 L480 310 L478 280",
      delay: 24,
    },
  ];
  return (
    <svg width={900} height={620} viewBox="0 0 900 620" style={{ overflow: "visible" }}>
      <g transform={`translate(450 600) scale(${sc}) translate(-450 -600)`}>
        <polygon
          points="150,180 450,180 420,128 168,140"
          fill={BOX_L}
          stroke={BOX_LINE}
          strokeWidth={6}
          strokeLinejoin="round"
        />
        <polygon
          points="750,180 450,180 480,128 732,140"
          fill={BOX_L}
          stroke={BOX_LINE}
          strokeWidth={6}
          strokeLinejoin="round"
        />
        <rect x={150} y={180} width={600} height={400} rx={10} fill={BOX} stroke={BOX_LINE} strokeWidth={6} />
        <rect x={185} y={215} width={530} height={330} rx={6} fill={BOX_IN} />
        <ellipse cx={450} cy={450} rx={220} ry={150} fill="#ffb36b" opacity={glow} />
        <g transform="translate(360 360) scale(0.9)">
          <CatPaths color={INK} eyes="closed" />
        </g>
      </g>
      {loops.map((l, i) => (
        <g key={i}>
          <Draw d={l.d} delay={l.delay} duration={18} stroke={CREAM} width={10} />
          <Draw d={l.head} delay={l.delay + 17} duration={5} stroke={CREAM} width={10} />
        </g>
      ))}
    </svg>
  );
};

const S3Blanket: React.FC = () => (
  <>
    <Safe gap={40}>
      <Kinetic text={"SELIMUT\n*GRATIS*"} size={130} delay={6} stagger={6} />
      <HeatBox />
    </Safe>
    <Sfx name="pop" at={14} volume={0.35} />
  </>
);

/* ------------------------------------------------------------------ */
/* Scene 4                                                             */
/* ------------------------------------------------------------------ */

const pacPath = (cx: number, cy: number, r: number, s: number) => {
  const rad = (a: number) => (a * Math.PI) / 180;
  const x1 = cx + r * Math.cos(rad(s + 90));
  const y1 = cy + r * Math.sin(rad(s + 90));
  const x2 = cx + r * Math.cos(rad(s));
  const y2 = cy + r * Math.sin(rad(s));
  return `M${cx} ${cy} L${x1.toFixed(2)} ${y1.toFixed(2)} A${r} ${r} 0 1 1 ${x2.toFixed(2)} ${y2.toFixed(
    2
  )} Z`;
};

const PacMan: React.FC<{ cx: number; cy: number; r: number; s: number; delay: number }> = ({
  cx,
  cy,
  r,
  s,
  delay,
}) => {
  const e = useEnter(Math.max(0, delay));
  return (
    <path
      d={pacPath(cx, cy, r, s)}
      fill={INK}
      transform={`translate(${cx} ${cy}) scale(${Math.max(0, e)}) translate(${-cx} ${-cy})`}
    />
  );
};

const PacFloor: React.FC<{
  w?: number;
  delays: number[];
  dashAt?: number;
  catAt?: number;
  blink?: boolean;
}> = ({ w = 760, delays, dashAt, catAt, blink }) => {
  const f = useCurrentFrame();
  const plate = useEnter(0);
  const C: [number, number][] = [
    [200, 110],
    [560, 110],
    [560, 370],
    [200, 370],
  ];
  const starts = [0, 90, 180, 270];
  const lp = dashAt === undefined ? 0 : interpolate(f, [dashAt, dashAt + 24], [0, 1], CL);
  const fillOp = interpolate(lp, [0.8, 1], [0, 0.75], CL);
  const lineOp = blink ? (Math.floor(f / 5) % 2 === 0 ? 1 : 0.2) : 1;
  const ps = 0.9 + 0.1 * cl(plate, 0, 1.05);

  let cat: React.ReactNode = null;
  if (catAt !== undefined && f >= catAt) {
    const wp = interpolate(f, [catAt, catAt + 22], [0, 1], { ...CL, easing: Easing.out(Easing.quad) });
    const x = interpolate(wp, [0, 1], [700, 290]);
    const walking = wp < 1;
    const bobY = walking ? -Math.abs(Math.sin(f / 2.2)) * 14 : 0;
    const sit = interpolate(f, [catAt + 22, catAt + 27, catAt + 34], [1, 0.86, 1], CL);
    const op = interpolate(f, [catAt, catAt + 5], [0, 1], CL);
    cat = (
      <g
        opacity={op}
        transform={`translate(${x} ${148 + bobY}) translate(90 176) scale(${2 - sit} ${sit}) translate(-90 -176) scale(0.9)`}
      >
        <CatPaths color={GINGER} eyes={walking ? "open" : "happy"} />
      </g>
    );
  }

  return (
    <svg width={w} height={(w * 480) / 760} viewBox="0 0 760 480" style={{ overflow: "visible" }}>
      <g transform={`translate(380 240) scale(${ps}) translate(-380 -240)`}>
        <rect x={40} y={20} width={680} height={440} rx={44} fill="#f3e7d3" stroke={INK} strokeOpacity={0.08} strokeWidth={4} />
        <rect x={200} y={110} width={360} height={260} fill="#ffffff" opacity={fillOp} />
        {dashAt !== undefined
          ? C.map(([x1, y1], i) => {
              const [x2, y2] = C[(i + 1) % 4];
              const e = cl(lp * 4 - i, 0, 1);
              if (e <= 0) return null;
              return (
                <line
                  key={i}
                  x1={x1}
                  y1={y1}
                  x2={x1 + (x2 - x1) * e}
                  y2={y1 + (y2 - y1) * e}
                  stroke={ORANGE}
                  strokeWidth={9}
                  strokeDasharray="24 16"
                  strokeLinecap="round"
                  opacity={lineOp}
                />
              );
            })
          : null}
        {C.map(([cx, cy], i) => (
          <PacMan key={i} cx={cx} cy={cy} r={56} s={starts[i]} delay={delays[i] ?? 0} />
        ))}
        {cat}
      </g>
    </svg>
  );
};

const S4Study: React.FC<{ pacs: number[] }> = ({ pacs }) => (
  <>
    <FoldFrame />
    <Safe gap={34}>
      <PawNum n={3} delay={2} size={130} />
      <StudyCard title="STUDI 2021" sub="Smith dkk." delay={16} chip="kotak bohongan" chipDelay={30} />
      <PacFloor w={700} delays={pacs} />
    </Safe>
    <Sfx name="pop" at={18} volume={0.45} />
    {pacs.map((p, i) => (
      <Sfx key={i} name="tick" at={Math.max(0, p)} volume={0.25} />
    ))}
  </>
);

const S4Illusion: React.FC<{ word: number }> = ({ word }) => (
  <>
    <Safe gap={44}>
      <Punch at={word + 4}>
        <Kinetic text={"KOTAK *ILUSI*"} size={120} delay={Math.max(0, word - 4)} stagger={6} />
      </Punch>
      <PacFloor w={860} delays={[0, 2, 4, 6]} dashAt={8} catAt={30} />
    </Safe>
    <Sfx name="pop" at={52} volume={0.45} />
    <Sfx name="pop" at={58} volume={0.35} />
  </>
);

const BigPill: React.FC<{ text: string; delay: number }> = ({ text, delay }) => {
  const e = useEnter(delay);
  const f = useCurrentFrame();
  const wob = Math.sin(f / 5) * 1.5;
  return (
    <div
      style={{
        transform: `scale(${Math.max(0, e)}) rotate(${-3 + wob}deg)`,
        background: INK,
        color: "#fff",
        fontFamily: FONT,
        fontWeight: 900,
        fontSize: 66,
        padding: "26px 54px",
        borderRadius: 999,
        display: "flex",
        alignItems: "center",
        gap: 18,
        boxShadow: "0 20px 0 rgba(0,0,0,0.2)",
        whiteSpace: "nowrap",
      }}
    >
      <Icon name="sparkle" size={56} color="#ffd34d" />
      {text}
    </div>
  );
};

const S4Bound: React.FC = () => (
  <>
    <Safe gap={44}>
      <Kinetic text={"CUMA BUTUH\n*BATAS*"} size={120} delay={0} stagger={5} />
      <PacFloor w={680} delays={[0, 0, 0, 0]} dashAt={-40} catAt={-60} blink />
      <BigPill text="IF I FITS, I SITS" delay={14} />
    </Safe>
    <Sfx name="pop" at={15} volume={0.45} />
  </>
);

/* ------------------------------------------------------------------ */
/* Scene 5                                                             */
/* ------------------------------------------------------------------ */

const CastleMorph: React.FC<{ at: number }> = ({ at }) => {
  const f = useCurrentFrame();
  const th = useTheme();
  const a = Math.max(1, at);
  const m = interpolate(f, [a, a + 14], [0, 1], { ...CL, easing: EASE });
  const bounce = interpolate(f, [a + 10, a + 15, a + 22], [1, 1.05, 1], CL);
  const enter = cl(useEnter(0), 0, 1.05);
  const peek = interpolate(f, [6, 20], [0, 1], { ...CL, easing: EASE });
  const wave = Math.sin(f / 4) * 10;
  const burst = interpolate(f, [a + 8, a + 14, a + 28], [0, 1, 0], CL);
  const towerMerlons = (x0: number) =>
    [0, 1, 2].map((i) => (
      <rect
        key={i}
        x={x0 + i * 42}
        y={116}
        width={26}
        height={36}
        fill={TOWER}
        stroke={BOX_LINE}
        strokeWidth={5}
      />
    ));
  return (
    <svg width={640} height={500} viewBox="0 0 640 500" style={{ overflow: "visible" }}>
      <g transform={`translate(320 470) scale(${enter * bounce}) translate(-320 -470)`}>
        <ellipse cx={320} cy={472} rx={290} ry={16} fill="#000" opacity={0.25} />
        {[0, 1, 2, 3, 4, 5, 6, 7].map((i) => {
          const ang = (i / 8) * Math.PI * 2;
          return (
            <line
              key={i}
              x1={320 + Math.cos(ang) * 290}
              y1={270 + Math.sin(ang) * 230}
              x2={320 + Math.cos(ang) * 330}
              y2={270 + Math.sin(ang) * 262}
              stroke={th.accent}
              strokeWidth={10}
              strokeLinecap="round"
              opacity={burst}
            />
          );
        })}
        <g transform={`translate(240 ${300 - 170 * peek}) scale(0.8)`}>
          <CatPaths color={GINGER} eyes="happy" />
        </g>
        {m > 0.01 ? (
          <g transform={`translate(0 470) scale(1 ${m}) translate(0 -470)`}>
            <rect x={92} y={150} width={110} height={320} fill={TOWER} stroke={BOX_LINE} strokeWidth={5} />
            <rect x={438} y={150} width={110} height={320} fill={TOWER} stroke={BOX_LINE} strokeWidth={5} />
            {towerMerlons(92)}
            {towerMerlons(438)}
            <path d="M132 260 V236 A15 15 0 0 1 162 236 V260 Z" fill={BOX_IN} />
            <path d="M478 260 V236 A15 15 0 0 1 508 236 V260 Z" fill={BOX_IN} />
            <line x1={493} y1={118} x2={493} y2={38} stroke={BOX_LINE} strokeWidth={6} strokeLinecap="round" />
            <path
              d={`M493 40 Q533 ${40 + wave} 575 ${56 + wave * 0.3} Q533 ${72 - wave} 493 76 Z`}
              fill={th.accent}
            />
          </g>
        ) : null}
        <rect x={170} y={260} width={300} height={210} fill={BOX} stroke={BOX_LINE} strokeWidth={5} />
        <g transform={`translate(0 262) scale(1 ${m}) translate(0 -262)`}>
          {[170, 236, 302, 368, 434].map((x) => (
            <rect key={x} x={x} y={226} width={36} height={36} fill={BOX} stroke={BOX_LINE} strokeWidth={5} />
          ))}
        </g>
        <g opacity={1 - m}>
          <polygon
            points="170,260 250,260 215,200 135,210"
            fill={BOX_L}
            stroke={BOX_LINE}
            strokeWidth={5}
            strokeLinejoin="round"
          />
          <polygon
            points="470,260 390,260 425,200 505,210"
            fill={BOX_L}
            stroke={BOX_LINE}
            strokeWidth={5}
            strokeLinejoin="round"
          />
          <rect x={302} y={262} width={36} height={60} fill="#f2cf98" />
        </g>
        <path d="M270 470 V395 A50 50 0 0 1 370 395 V470 Z" fill={BOX_IN} opacity={m} />
      </g>
    </svg>
  );
};

const SummaryItem: React.FC<{ kind: "shield" | "fire" | "dash"; label: string; delay: number }> = ({
  kind,
  label,
  delay,
}) => (
  <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 14 }}>
    {kind === "shield" ? (
      <ShieldBadge size={120} delay={delay} />
    ) : kind === "fire" ? (
      <IconBadge name="fire" size={120} bg={ORANGE} delay={delay} />
    ) : (
      <DashBadge size={120} delay={delay} />
    )}
    <Pop delay={delay + 4} from="up">
      <T size={38} weight={900}>
        {label}
      </T>
    </Pop>
  </div>
);

const S5Castle: React.FC<{ at: number; kardus: number }> = ({ at, kardus }) => (
  <>
    <Safe gap={34}>
      <Kinetic text={"KARDUS =\n*KASTIL*"} size={124} delay={Math.max(0, kardus - 4)} stagger={14} />
      <CastleMorph at={at} />
      <div style={{ display: "flex", gap: 60 }}>
        <SummaryItem kind="shield" label="AMAN" delay={at + 12} />
        <SummaryItem kind="fire" label="HANGAT" delay={at + 17} />
        <SummaryItem kind="dash" label="ILUSI" delay={at + 22} />
      </div>
    </Safe>
    <Sfx name="pop" at={Math.max(0, at + 2)} volume={0.45} />
  </>
);

const MiniBox: React.FC = () => (
  <g>
    <polygon points="-110,-50 -70,-80 70,-80 110,-50" fill={BOX_IN} stroke={BOX_LINE} strokeWidth={5} />
    <rect x={-110} y={-50} width={220} height={150} fill={BOX} stroke={BOX_LINE} strokeWidth={5} />
    <polygon points="-110,-50 -70,-80 -130,-112 -160,-82" fill={BOX_L} stroke={BOX_LINE} strokeWidth={5} />
    <polygon points="110,-50 70,-80 130,-112 160,-82" fill={BOX_L} stroke={BOX_LINE} strokeWidth={5} />
    <rect x={-16} y={-50} width={32} height={50} fill="#f2cf98" />
  </g>
);

const TrashSave: React.FC = () => {
  const f = useCurrentFrame();
  const e = cl(useEnter(0), 0, 1.05);
  const s = interpolate(f, [16, 34], [0, 1], { ...CL, easing: EASE });
  const bx = interpolate(s, [0, 1], [350, 590]);
  const by = interpolate(s, [0, 1], [330, 160]);
  const br = interpolate(s, [0, 1], [0, -12]);
  const lid = interpolate(f, [12, 20], [0, -30], { ...CL, easing: EASE });
  return (
    <svg width={760} height={560} viewBox="0 0 760 600" style={{ overflow: "visible" }}>
      <g transform={`translate(350 580) scale(${e}) translate(-350 -580)`}>
        <g transform={`translate(${bx} ${by}) rotate(${br}) scale(0.75)`}>
          <MiniBox />
        </g>
        <path d="M220 260 L480 260 L455 570 L245 570 Z" fill="#7d8597" stroke="#3d4350" strokeWidth={6} strokeLinejoin="round" />
        {[290, 350, 410].map((x) => (
          <line key={x} x1={x} y1={300} x2={x} y2={530} stroke="#3d4350" strokeWidth={8} strokeLinecap="round" opacity={0.5} />
        ))}
        <g transform={`rotate(${lid} 200 252)`}>
          <rect x={200} y={222} width={300} height={36} rx={12} fill="#8f97a8" stroke="#3d4350" strokeWidth={6} />
          <rect x={315} y={200} width={70} height={26} rx={10} fill="#8f97a8" stroke="#3d4350" strokeWidth={6} />
        </g>
      </g>
      <Draw d="M170 230 L530 590" delay={4} duration={10} stroke={RED} width={30} />
      <Draw d="M530 230 L170 590" delay={10} duration={10} stroke={RED} width={30} />
    </svg>
  );
};

const S5Trash: React.FC = () => (
  <>
    <Safe gap={34}>
      <Kinetic text={"JANGAN\n*DIBUANG*"} size={130} delay={0} stagger={5} />
      <TrashSave />
      <Chip text="kardus diselamatkan" icon="heart" tone="accent" delay={30} />
    </Safe>
    <Sfx name="pop" at={18} volume={0.4} />
  </>
);

const CommentCard: React.FC = () => {
  const f = useCurrentFrame();
  const e = useEnter(4);
  const atE = useEnter(12);
  const msg = "@sahabatmu lihat, kastil!";
  const n = Math.floor(interpolate(f, [18, 38], [0, msg.length], CL));
  const typed = msg.slice(0, n);
  const mention = typed.slice(0, 10);
  const rest = typed.slice(10);
  const caret = Math.floor(f / 6) % 2 === 0 ? 1 : 0;
  const pulse = f > 30 ? 1 + 0.07 * Math.abs(Math.sin((f - 30) / 4)) : 1;
  const k = cl(e * 1.4, 0, 1);
  const gray = "#8a8378";
  return (
    <div
      style={{
        width: 860,
        background: "#ffffff",
        borderRadius: 44,
        padding: 40,
        fontFamily: FONT,
        color: INK,
        boxShadow: "0 30px 70px rgba(0,0,0,0.25)",
        transform: `translateY(${(1 - e) * 140}px)`,
        opacity: k,
        display: "flex",
        flexDirection: "column",
        gap: 28,
      }}
    >
      <div style={{ display: "flex", alignItems: "baseline", gap: 16 }}>
        <div style={{ fontSize: 42, fontWeight: 900 }}>Komentar</div>
        <div style={{ fontSize: 32, fontWeight: 700, color: gray }}>1,2 rb</div>
      </div>
      <div style={{ display: "flex", gap: 22, alignItems: "center" }}>
        <div style={{ width: 80, height: 80, borderRadius: "50%", background: GINGER, flexShrink: 0 }} />
        <div>
          <div style={{ fontSize: 30, fontWeight: 800, color: gray }}>rina.meong</div>
          <div style={{ fontSize: 38, fontWeight: 700 }}>kucingku juga gitu wkwk</div>
        </div>
      </div>
      <div style={{ display: "flex", gap: 22, alignItems: "center" }}>
        <div
          style={{
            width: 80,
            height: 80,
            borderRadius: "50%",
            background: ORANGE,
            color: "#fff",
            fontSize: 50,
            fontWeight: 900,
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            transform: `scale(${Math.max(0, atE)})`,
            flexShrink: 0,
          }}
        >
          @
        </div>
        <div style={{ fontSize: 40, fontWeight: 800 }}>
          <span style={{ color: ORANGE }}>{mention}</span>
          <span>{rest}</span>
          <span style={{ opacity: caret, color: ORANGE }}>|</span>
        </div>
      </div>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <div style={{ display: "flex", alignItems: "center", gap: 10, color: gray, fontSize: 34, fontWeight: 800 }}>
          <Icon name="heart" size={44} color={ORANGE} />
          4,8 rb
        </div>
        <div
          style={{
            background: ORANGE,
            color: "#fff",
            fontSize: 42,
            fontWeight: 900,
            padding: "18px 40px",
            borderRadius: 999,
            display: "flex",
            alignItems: "center",
            gap: 12,
            transform: `scale(${pulse})`,
            boxShadow: "0 12px 0 rgba(0,0,0,0.15)",
          }}
        >
          <Icon name="plus" size={40} color="#fff" />
          Follow
        </div>
      </div>
    </div>
  );
};

const S5Tag: React.FC = () => (
  <>
    <Safe gap={44}>
      <Kinetic text={"TAG\n*TEMANMU*"} size={140} delay={0} stagger={6} />
      <Float>
        <CommentCard />
      </Float>
    </Safe>
    <Sfx name="pop" at={12} volume={0.35} />
    <Sfx name="pop" at={42} volume={0.5} />
  </>
);

/* ------------------------------------------------------------------ */
/* Scenes                                                              */
/* ------------------------------------------------------------------ */

const Scene1: SceneComponent = ({ durationInFrames, words }) => {
  const D = durationInFrames;
  const c1 = cl(wordAt(words, "kucingmu", 57), 45, D - 45);
  const land = Math.max(14, wordAt(words, "kardusnya", 89) - c1);
  return (
    <Shots
      durationInFrames={D}
      cuts={[c1]}
      shots={[
        { theme: "cream", content: <S1Product /> },
        { theme: "accent", content: <S1Win land={land} /> },
      ]}
    />
  );
};

const Scene2: SceneComponent = ({ durationInFrames, words }) => {
  const D = durationInFrames;
  const c1 = cl(wordAt(words, "Studi", 98) - 8, 45, 90);
  const c2 = cl(wordAt(words, "kucing", 176), c1 + 45, c1 + 90);
  const c3 = cl(wordAt(words, "tenang", 248), c2 + 45, D - 45);
  const aman = Math.max(26, wordAt(words, "aman", 59));
  const ding = Math.max(2, wordAt(words, "Studi", 98) - c1);
  return (
    <Shots
      durationInFrames={D}
      cuts={[c1, c2, c3]}
      shots={[
        { theme: "dark", content: <S2Safe aman={aman} /> },
        { theme: "cream", content: <S2Study ding={ding} /> },
        { theme: "night", content: <S2Stress /> },
        { theme: "accent", content: <S2Calm /> },
      ]}
    />
  );
};

const Scene3: SceneComponent = ({ durationInFrames, words }) => {
  const D = durationInFrames;
  const c1 = cl(wordAt(words, "nyaman", 83), 45, 90);
  const c2 = cl(wordAt(words, "derajat", 155), c1 + 45, c1 + 90);
  const c3 = cl(wordAt(words, "menahan", 204), c2 + 45, D - 45);
  const hangat = Math.max(4, wordAt(words, "hangat", 32));
  const t30 = Math.max(1, wordAt(words, "30", 96) - c1);
  const t38 = Math.max(t30 + 4, wordAt(words, "38", 127) - c1);
  return (
    <Shots
      durationInFrames={D}
      cuts={[c1, c2, c3]}
      shots={[
        { theme: "dark", content: <S3Hot hangat={hangat} /> },
        { theme: "cream", content: <S3Thermo startAt={t30} endAt={t38} /> },
        { theme: "night", content: <S3Compare /> },
        { theme: "accent", content: <S3Blanket /> },
      ]}
    />
  );
};

const Scene4: SceneComponent = ({ durationInFrames, words }) => {
  const D = durationInFrames;
  const c1 = cl(wordAt(words, "bohongan", 94) - 6, 45, 90);
  const c2 = cl(wordAt(words, "batasnya", 168), c1 + 45, D - 45);
  const p1 = wordAt(words, "kucing", 33);
  const p2 = wordAt(words, "bahkan", 44);
  const p3 = wordAt(words, "duduk", 54);
  const p4 = wordAt(words, "gambar", 71);
  const pacs = [p1, p2, p3, p4].map((p) => cl(p, 24, c1 - 12));
  const boh = Math.max(0, wordAt(words, "bohongan", 94) - c1);
  return (
    <Shots
      durationInFrames={D}
      cuts={[c1, c2]}
      shots={[
        { theme: "cream", content: <S4Study pacs={pacs} /> },
        { theme: "night", content: <S4Illusion word={boh} /> },
        { theme: "accent", content: <S4Bound /> },
      ]}
    />
  );
};

const Scene5: SceneComponent = ({ durationInFrames, words }) => {
  const D = durationInFrames;
  const c1 = cl(wordAt(words, "Jangan", 102) - 12, 45, 90);
  const c2 = cl(wordAt(words, "tag", 142), c1 + 45, D - 45);
  const at = cl(wordAt(words, "kastil", 59) - 5, 20, c1 - 30);
  const kardus = Math.max(4, wordAt(words, "kardus", 31));
  return (
    <Shots
      durationInFrames={D}
      cuts={[c1, c2]}
      shots={[
        { theme: "dark", content: <S5Castle at={at} kardus={kardus} /> },
        { theme: "cream", content: <S5Trash /> },
        { theme: "accent", content: <S5Tag /> },
      ]}
    />
  );
};

export const SCENES: SceneComponent[] = [Scene1, Scene2, Scene3, Scene4, Scene5];
