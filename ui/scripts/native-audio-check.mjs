// Headless check of src/lib/native-audio.ts (the Windows app's device sound) without Windows:
// node scripts/native-audio-check.mjs [out-dir]
// Bundles the module with Vite (sonner swapped for a shim that records toasts), serves it on
// 127.0.0.1 behind a fake Wails bridge whose DeviceAudioStart streams a 440 Hz tone as
// "tmx:devaudio" chunks like the Go side does, and measures the track nativeDeviceAudio() gives
// with an AnalyserNode in Playwright's bundled Chromium: it must carry the tone, go silent at
// Pause, come back at Resume and after the device dropped out, resample a device at another
// rate, work through the ScriptProcessorNode fallback, and stop the device when the track stops.
// On a shared machine wrap it: tmx browser-slot -- node scripts/native-audio-check.mjs
import http from "node:http";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "vite";
import { chromium } from "playwright";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const out = path.resolve(process.argv[2] ?? path.join(os.tmpdir(), "tmx-native-audio-check"));
fs.mkdirSync(out, { recursive: true });

await build({
  configFile: false,
  root,
  logLevel: "warn",
  resolve: { alias: { "@": path.join(root, "src") } },
  build: {
    outDir: out,
    emptyOutDir: true,
    minify: false,
    lib: { entry: path.join(root, "src/lib/native-audio.ts"), formats: ["es"], fileName: () => "native-audio.js" },
    rollupOptions: { external: ["sonner"] },
  },
});

const SHIM = `export const toast = Object.assign((...a) => (window.toasts ??= []).push(["info", ...a]), {
  warning: (...a) => (window.toasts ??= []).push(["warning", ...a]),
  error: (...a) => (window.toasts ??= []).push(["error", ...a]),
  success: (...a) => (window.toasts ??= []).push(["success", ...a]),
});`;

// The fake window process: DeviceAudioStart starts a 100 ms timer of tone chunks.
const BRIDGE = `
window.fake = { starts: 0, stops: 0, pings: 0, pingResult: null, listeners: {}, timer: null, seq: 0, frame: 0,
  emitRate: 48000, reportRate: 48000, channels: 2, freq: 440, startError: null };
const emit = (name, ...data) => (fake.listeners[name] || []).slice().forEach((cb) => cb(...data));
window.fakeEmit = emit;
window.runtime = { EventsOn(name, cb) { (fake.listeners[name] ||= []).push(cb); return () => { fake.listeners[name] = fake.listeners[name].filter((x) => x !== cb); }; } };
function chunk() {
  const rate = fake.emitRate, ch = fake.channels, n = rate / 10, s = new Int16Array(n * ch);
  for (let i = 0; i < n; i++) {
    const v = Math.round(0.5 * Math.sin(2 * Math.PI * fake.freq * (fake.frame + i) / rate) * 32767);
    s[i * ch] = v; if (ch > 1) s[i * ch + 1] = Math.round(v * 0.8); // the right channel a little softer
  }
  fake.frame += n;
  const b = new Uint8Array(s.buffer); let bin = "";
  for (let i = 0; i < b.length; i++) bin += String.fromCharCode(b[i]);
  return { seq: ++fake.seq, rate, channels: ch, frames: n, pcm: btoa(bin) };
}
window.go = { desktop: { App: {
  async DeviceAudioStart() {
    fake.starts++;
    if (fake.startError) throw new Error(fake.startError);
    if (!fake.timer) { fake.seq = 0; fake.timer = setInterval(() => emit("tmx:devaudio", chunk()), 100); }
    return { rate: fake.reportRate, channels: fake.channels };
  },
  async DeviceAudioPing() {
    fake.pings++;
    if (fake.pingResult !== null) { const r = fake.pingResult; fake.pingResult = null; if (!r) { clearInterval(fake.timer); fake.timer = null; } return r; }
    return !!fake.timer;
  },
  async DeviceAudioStop() { fake.stops++; clearInterval(fake.timer); fake.timer = null; },
} } };
`;

const METER = `
// Each channel on its own analyser (one analyser would mix them down to mono).
window.meterOn = (track) => {
  const ac = new AudioContext();
  const split = ac.createChannelSplitter(2);
  const an = [ac.createAnalyser(), ac.createAnalyser()];
  an.forEach((a, i) => { a.fftSize = 8192; split.connect(a, i); });
  ac.createMediaStreamSource(new MediaStream([track])).connect(split);
  window.meter = { ac, an, track };
};
const rmsOf = (a) => { const td = new Float32Array(a.fftSize); a.getFloatTimeDomainData(td); let s = 0; for (const v of td) s += v * v; return Math.sqrt(s / td.length); };
window.measure = () => {
  const { an, ac, track } = window.meter;
  const fd = new Float32Array(an[0].frequencyBinCount); an[0].getFloatFrequencyData(fd);
  let bi = 1; for (let i = 1; i < fd.length; i++) if (fd[i] > fd[bi]) bi = i;
  return { rms: rmsOf(an[0]), right: rmsOf(an[1]), peakHz: (bi * ac.sampleRate) / an[0].fftSize, ready: track.readyState };
};
window.sleep = (ms) => new Promise((r) => setTimeout(r, ms));
// RMS every 100 ms for n samples
window.series = async (n) => { const o = []; for (let i = 0; i < n; i++) { await sleep(100); o.push(measure()); } return o; };
`;

const page = (bridge) => `<!doctype html><meta charset="utf-8"><title>native-audio check</title>
<script type="importmap">{"imports":{"sonner":"/sonner-shim.js"}}</script>
${bridge ? '<script src="/fake-bridge.js"></script>' : ""}
<script src="/meter.js"></script>
<script type="module">import * as na from "/native-audio.js"; window.na = na; window.naReady = true;</script>`;

const files = {
  "/": ["text/html", page(true)],
  "/nobridge.html": ["text/html", page(false)],
  "/sonner-shim.js": ["text/javascript", SHIM],
  "/fake-bridge.js": ["text/javascript", BRIDGE],
  "/meter.js": ["text/javascript", METER],
  "/native-audio.js": ["text/javascript", fs.readFileSync(path.join(out, "native-audio.js"), "utf8")],
};
const server = http.createServer((req, res) => {
  const f = files[new URL(req.url, "http://x").pathname];
  if (!f) return res.writeHead(404).end();
  res.writeHead(200, { "Content-Type": f[0] }).end(f[1]);
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const base = `http://127.0.0.1:${server.address().port}`;

const WIN_UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0";
const MAC_UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko)";

let failures = 0;
const check = (ok, what, detail) => {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}${detail === undefined ? "" : " " + JSON.stringify(detail)}`);
  if (!ok) failures++;
};
// The tone: 0.5 amplitude on the left (RMS 0.354), 0.8 of it on the right; 440 Hz.
const toneOK = (m) => Math.abs(m.rms - 0.354) < 0.04 && Math.abs(m.right / m.rms - 0.8) < 0.05 && Math.abs(m.peakHz - 440) < 15;
const silent = (m) => m.rms < 0.001 && m.right < 0.001;

const browser = await chromium.launch({
  headless: true,
  args: ["--disable-features=WebAuthenticationUseNativeWinApi", "--autoplay-policy=no-user-gesture-required"],
});
async function open(ua, url = "/") {
  const ctx = await browser.newContext({ userAgent: ua });
  const p = await ctx.newPage();
  const cdp = await ctx.newCDPSession(p);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true },
  });
  p.on("pageerror", (e) => check(false, "page error", e.message));
  await p.goto(base + url);
  await p.waitForFunction(() => window.naReady === true);
  return { ctx, p };
}

try {
  // ---- the Windows app
  const { ctx, p } = await open(WIN_UA);
  check((await p.evaluate(() => fake.stops)) === 1, "a page that loads stops a capture left running");
  check(await p.evaluate(() => na.hasNativeDeviceAudio()), "hasNativeDeviceAudio in the Windows app");

  let r = await p.evaluate(async () => {
    const track = await na.nativeDeviceAudio({ pingMs: 300 });
    if (!track) return null;
    meterOn(track);
    await sleep(1200);
    return { kind: track.kind, starts: fake.starts, series: await series(10) };
  });
  check(r && r.kind === "audio" && r.starts === 1, "nativeDeviceAudio gives an audio track and starts the device once", r && { kind: r.kind, starts: r.starts });
  check(r && r.series.every(toneOK), "the track carries the 440 Hz tone, without gaps (worklet)", r?.series.map((m) => [m.rms.toFixed(3), Math.round(m.peakHz)]));
  check((await p.evaluate(() => fake.pings)) >= 2, "the page renews the lease");
  check(await p.evaluate(async () => (await na.nativeDeviceAudio()) === meter.track && fake.starts === 1), "asking again gives the same track");

  r = await p.evaluate(async () => {
    await na.pauseNativeDeviceAudio(true);
    const stopped = fake.stops === 2 && fake.timer === null;
    await sleep(500);
    const paused = measure();
    await na.pauseNativeDeviceAudio(false);
    await sleep(900);
    return { stopped, paused, starts: fake.starts, resumed: await series(5) };
  });
  check(r.stopped, "Pause stops the device");
  check(silent(r.paused), "paused: the track is silent", r.paused.rms);
  check(r.starts === 2 && r.resumed.every(toneOK), "Resume starts the device again and the tone is back", r.resumed.map((m) => m.rms.toFixed(3)));

  r = await p.evaluate(async () => {
    fake.pingResult = false; // the device went away: the next ping says the capture ended
    fakeEmit("tmx:devaudio-ended", "the sound output device went away");
    await sleep(1500);
    return { starts: fake.starts, toasts: window.toasts ?? [], series: await series(5) };
  });
  check(r.starts === 3, "a capture that ended by itself is started again", r.starts);
  check(r.toasts.some((t) => t[0] === "warning" && /device sound stopped/.test(t[1])), "the owner is told the device sound stopped", r.toasts);
  check(r.series.every(toneOK), "the tone is back after the device returned", r.series.map((m) => m.rms.toFixed(3)));

  r = await p.evaluate(async () => {
    meter.track.stop();
    await sleep(300);
    return { stops: fake.stops, timer: fake.timer, listeners: (fake.listeners["tmx:devaudio"] || []).length, ended: (fake.listeners["tmx:devaudio-ended"] || []).length };
  });
  check(r.stops === 3 && r.timer === null && r.listeners === 0 && r.ended === 0, "stopping the track stops the device and the listeners", r);

  // A device at 44.1 kHz in a 48 kHz context: the player resamples; the tone stays at 440 Hz.
  r = await p.evaluate(async () => {
    fake.emitRate = 44100;
    const track = await na.nativeDeviceAudio({ pingMs: 300 });
    meterOn(track);
    await sleep(1200);
    const s = await series(8);
    na.stopNativeDeviceAudio();
    fake.emitRate = 48000;
    return { s, stopped: fake.timer === null };
  });
  check(r.s.every(toneOK), "a device at 44.1 kHz is resampled (still 440 Hz)", r.s.map((m) => [m.rms.toFixed(3), Math.round(m.peakHz)]));
  check(r.stopped, "stopNativeDeviceAudio stops the device");

  // The ScriptProcessorNode fallback.
  r = await p.evaluate(async () => {
    const track = await na.nativeDeviceAudio({ worklet: false, pingMs: 300 });
    meterOn(track);
    await sleep(1200);
    const s = await series(8);
    track.stop();
    return s;
  });
  check(r.every(toneOK), "the ScriptProcessorNode fallback carries the tone", r.map((m) => [m.rms.toFixed(3), Math.round(m.peakHz)]));

  // A MediaRecorder takes the track (as the recorder will, mixed or alone).
  r = await p.evaluate(async () => {
    const track = await na.nativeDeviceAudio();
    const mr = new MediaRecorder(new MediaStream([track]), { mimeType: "audio/webm;codecs=opus" });
    const parts = [];
    mr.ondataavailable = (e) => e.data.size && parts.push(e.data);
    mr.start(250);
    await sleep(1500);
    await new Promise((res) => { mr.onstop = res; mr.stop(); });
    track.stop();
    return parts.reduce((a, b) => a + b.size, 0);
  });
  check(r > 2000, "a MediaRecorder records the track", r);

  // The device cannot open: null, nothing left running.
  r = await p.evaluate(async () => {
    fake.startError = "this PC has no sound output device";
    const t = await na.nativeDeviceAudio();
    fake.startError = null;
    return { t: t === null, timer: fake.timer };
  });
  check(r.t && r.timer === null, "no device: nativeDeviceAudio gives null");

  // What each platform says in the start dialog.
  r = await p.evaluate(() => {
    const s = (e) => na.deviceAudioSupport(e);
    const CH = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36";
    const CHW = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36";
    const CHM = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36";
    const FF = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0";
    const SF = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15";
    return {
      winApp: s({ desktop: true, platform: "windows" }).route,
      macApp: s({ desktop: true, platform: "darwin" }).route,
      macAppNative: s({ desktop: true, platform: "darwin", nativeMac: true }).route,
      chromeWin: s({ desktop: false, platform: "windows", userAgent: CHW }).route,
      chromeMac: s({ desktop: false, platform: "darwin", userAgent: CHM }).route,
      chromeLinux: s({ desktop: false, platform: "linux", userAgent: CH }).route,
      firefox: s({ desktop: false, platform: "linux", userAgent: FF }).route,
      safari: s({ desktop: false, platform: "darwin", userAgent: SF }).route,
    };
  });
  check(
    JSON.stringify(r) ===
      JSON.stringify({ winApp: "loopback", macApp: "none", macAppNative: "native", chromeWin: "picker", chromeMac: "picker", chromeLinux: "picker", firefox: "none", safari: "none" }),
    "deviceAudioSupport per platform",
    r,
  );
  await ctx.close();

  // ---- not the Windows app: nothing starts
  const mac = await open(MAC_UA);
  r = await mac.p.evaluate(async () => ({ t: await na.nativeDeviceAudio(), starts: fake.starts, stops: fake.stops, has: na.hasNativeDeviceAudio() }));
  check(r.t === null && r.starts === 0 && r.stops === 0 && !r.has, "the macOS app: null, the bridge is not called", r);
  await mac.ctx.close();
  const plain = await open(WIN_UA, "/nobridge.html");
  r = await plain.p.evaluate(async () => ({ t: await na.nativeDeviceAudio(), has: na.hasNativeDeviceAudio() }));
  check(r.t === null && !r.has, "a browser on Windows: null", r);
  await plain.ctx.close();
} finally {
  await browser.close();
  server.close();
}
console.log(failures ? `${failures} check(s) failed` : "all checks passed");
process.exit(failures ? 1 : 0);
