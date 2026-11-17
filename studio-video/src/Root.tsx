import { Composition } from "remotion";
import { Intro } from "./Intro";
import { AutoVideo } from "./auto/AutoVideo";
import { job } from "./generated/job";
import { SCENES } from "./generated/Scenes";

// Scene diimpor langsung, BUKAN lewat defaultProps: Remotion mengubah props jadi JSON
// saat render, sehingga komponen (fungsi) akan hilang dan video jadi kosong.
const GeneratedVideo: React.FC = () => <AutoVideo job={job} scenes={SCENES} />;

export const RemotionRoot: React.FC = () => {
  const total = job.scenes.reduce((s, sc) => s + sc.durationInFrames, 0);
  return (
    <>
      <Composition
        id="Intro"
        component={Intro}
        durationInFrames={150}
        fps={30}
        width={1920}
        height={1080}
        defaultProps={{ title: "Halo, Remotion!", subtitle: "Video dari code" }}
      />
      <Composition
        id="AutoVideo"
        component={GeneratedVideo}
        durationInFrames={Math.max(1, total)}
        fps={job.fps}
        width={job.width}
        height={job.height}
      />
    </>
  );
};
