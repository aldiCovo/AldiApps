// Data yang dihasilkan pipeline otomatis untuk satu video.

/** Satu kata narasi beserta waktunya (frame, relatif terhadap awal scene). */
export type SceneWord = { t: string; s: number; e: number };

export type JobScene = {
  narasi: string;
  durationInFrames: number;
  audio: string | null; // path relatif terhadap public/
  words?: SceneWord[]; // timing per kata dari TTS (subtitle karaoke & sinkron animasi)
};

export type JobData = {
  id: string;
  fps: number;
  width: number;
  height: number;
  music: string | null; // path relatif terhadap public/
  sfx?: boolean; // true jika public/sfx/*.wav tersedia (whoosh antar-scene)
  scenes: JobScene[];
};

// Kontrak untuk kode yang ditulis Claude: setiap scene menerima durasinya + timing kata narasinya.
export type SceneComponent = React.FC<{ durationInFrames: number; words: SceneWord[] }>;
