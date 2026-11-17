import { Config } from "@remotion/cli/config";

Config.setVideoImageFormat("jpeg");
Config.setOverwriteOutput(true);
// i7 gen 12 punya banyak thread; Remotion pakai ini untuk render paralel.
Config.setConcurrency(8);
// Pakai GPU untuk efek CSS/WebGL di Chrome headless.
Config.setChromiumOpenGlRenderer("angle");
