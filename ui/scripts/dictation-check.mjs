// Headless check of src/lib/dictation-session.ts (#45: a dictation survives page switches):
// node scripts/dictation-check.mjs [out-dir]
// Bundles the module with Vite (sonner swapped for a shim that records toasts; lib/voice and
// lib/router for fakes: transcribe answers "hello world" and is counted), and drives it in
// Playwright's bundled Chromium with its fake microphone. It checks that a dictation keeps
// capturing after its message box leaves the screen and the window hides and shows again, that
// Stop then sends exactly one transcription whose words land in the box's draft and releases
// the microphone; that a box on screen gets the words directly; that only one dictation runs at
// a time; that stopping while the microphone permission is still pending records nothing and
// releases the late microphone; that a microphone going away keeps what was heard; and that
// closing the window asks while a dictation runs.
// On a shared machine wrap it: tmx browser-slot -- node scripts/dictation-check.mjs
import http from "node:http";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "vite";
import { chromium } from "playwright";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const out = path.resolve(process.argv[2] ?? path.join(os.tmpdir(), "tmx-dictation-check"));
fs.mkdirSync(out, { recursive: true });

const STUBS = {
  [path.join(root, "src/lib/voice.ts")]: `import { create } from "zustand";
export const useVoice = create(() => ({ status: { settings: { language: "auto" } }, loaded: true }));
export const voiceApi = {
  async transcribe(data, language) {
    (window.transcribes ??= []).push({ bytes: atob(data).length, language });
    await new Promise((r) => setTimeout(r, 50));
    return { text: "hello world", language: "en", segments: [] };
  },
};`,
  [path.join(root, "src/lib/router.ts")]: `export function navigate(p) { (window.navigations ??= []).push(p); }`,
};

await build({
  configFile: false,
  root,
  logLevel: "warn",
  define: { "process.env.NODE_ENV": JSON.stringify("production") }, // zustand, bundled in
  resolve: { alias: { "@": path.join(root, "src") } },
  plugins: [
    {
      name: "stubs",
      enforce: "pre",
      async resolveId(src, importer) {
        if (!importer) return null;
        const r = await this.resolve(src, importer, { skipSelf: true });
        return r && STUBS[r.id] ? "\0stub:" + r.id : null;
      },
      load: (id) => (id.startsWith("\0stub:") ? STUBS[id.slice(6)] : null),
    },
  ],
  build: {
    outDir: out,
    emptyOutDir: true,
    minify: false,
    lib: { entry: path.join(root, "src/lib/dictation-session.ts"), formats: ["es"], fileName: () => "dictation-session.js" },
    rollupOptions: { external: ["sonner"] },
  },
});

const SHIM = `export const toast = Object.assign((...a) => (window.toasts ??= []).push(["info", ...a]), {
  warning: (...a) => (window.toasts ??= []).push(["warning", ...a]),
  error: (...a) => (window.toasts ??= []).push(["error", ...a]),
  success: (...a) => (window.toasts ??= []).push(["success", ...a]),
});`;

// Counts microphone requests, can delay them (permission pending), and keeps the streams so the
// check can see whether the microphone is still held. document.hidden can be faked.
const HARNESS = `
window.sleep = (ms) => new Promise((r) => setTimeout(r, ms));
window.mics = []; window.micAsks = 0; window.micDelay = 0; window.transcribes = []; window.toasts = [];
const real = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
navigator.mediaDevices.getUserMedia = async (c) => {
  micAsks++;
  if (micDelay) await sleep(micDelay);
  const s = await real(c);
  mics.push(s);
  return s;
};
window.live = () => mics.filter((s) => s.getAudioTracks().some((t) => t.readyState === "live")).length;
window.fakeHidden = false;
Object.defineProperty(document, "hidden", { get: () => window.fakeHidden, configurable: true });
window.setHidden = (h) => { fakeHidden = h; document.dispatchEvent(new Event("visibilitychange")); };
window.drafts = new Map();
window.target = (id) => ({ id, label: "Agent " + id, path: "/agents/" + id, drafts, draftKey: id });
window.box = (id) => { const got = []; const un = ds.registerDictationBox(id, (t) => got.push(t)); return { got, un }; };
window.state = () => { const s = ds.useDictation.getState(); return { phase: s.phase, target: s.target?.id ?? null }; };
window.idle = async () => { for (let i = 0; i < 60 && ds.useDictation.getState().phase !== "idle"; i++) await sleep(50); };
`;

const PAGE = `<!doctype html><meta charset="utf-8"><title>dictation check</title>
<script type="importmap">{"imports":{"sonner":"/sonner-shim.js"}}</script>
<script src="/harness.js"></script>
<script type="module">import * as ds from "/dictation-session.js"; window.ds = ds; window.dsReady = true;</script>`;

const files = {
  "/": ["text/html", PAGE],
  "/sonner-shim.js": ["text/javascript", SHIM],
  "/harness.js": ["text/javascript", HARNESS],
  "/dictation-worklet.js": ["text/javascript", fs.readFileSync(path.join(root, "public/dictation-worklet.js"), "utf8")],
  "/dictation-session.js": ["text/javascript", fs.readFileSync(path.join(out, "dictation-session.js"), "utf8")],
};
const server = http.createServer((req, res) => {
  const f = files[new URL(req.url, "http://x").pathname];
  if (!f) return res.writeHead(404).end();
  res.writeHead(200, { "Content-Type": f[0] }).end(f[1]);
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const base = `http://127.0.0.1:${server.address().port}`;

let failures = 0;
const check = (ok, what, detail) => {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}${detail === undefined ? "" : " " + JSON.stringify(detail)}`);
  if (!ok) failures++;
};

const browser = await chromium.launch({
  headless: true,
  args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--autoplay-policy=no-user-gesture-required"],
});

try {
  const ctx = await browser.newContext();
  await ctx.grantPermissions(["microphone"], { origin: base });
  const p = await ctx.newPage();
  p.on("pageerror", (e) => check(false, "page error", e.message));
  await p.goto(base + "/");
  await p.waitForFunction(() => window.dsReady === true);

  // 1. The box leaves the screen (a page switch) and the window hides and shows again.
  let r = await p.evaluate(async () => {
    const b = box("a");
    await ds.startDictating(target("a"));
    await sleep(600);
    b.un(); // the page switched: the composer unmounted
    setHidden(true);
    await sleep(700);
    setHidden(false);
    await sleep(400);
    const mid = { ...state(), live: live(), asks: micAsks, transcribes: transcribes.length };
    await ds.stopDictating();
    return { mid, after: { ...state(), live: live(), transcribes: transcribes.length, bytes: transcribes[0]?.bytes ?? 0, draft: drafts.get("a"), got: b.got, toasts } };
  });
  check(r.mid.phase === "recording" && r.mid.target === "a", "still recording into the same box after it left the screen and the window hid", r.mid);
  check(r.mid.live === 1 && r.mid.asks === 1, "the same microphone stays open (one request, still live)", r.mid);
  check(r.mid.transcribes === 0, "nothing is transcribed or sent before Stop");
  check(r.after.phase === "idle" && r.after.target === null, "Stop ends it", r.after.phase);
  check(r.after.transcribes === 1 && r.after.bytes > 44 + 16000 * 2, "Stop transcribes exactly once, with the whole recording (> 1 s of audio)", { n: r.after.transcribes, bytes: r.after.bytes });
  check(r.after.draft === "hello world" && r.after.got.length === 0, "the words land in the original box's draft", r.after.draft);
  check(r.after.toasts.some((t) => /in your draft to Agent a/.test(t[1])), "the owner is told where the words went", r.after.toasts);
  check(r.after.live === 0, "the microphone is released after Stop");

  // 2. A box on screen gets the words at its cursor, not in the draft map.
  r = await p.evaluate(async () => {
    const b = box("b");
    await ds.startDictating(target("b"));
    await sleep(600);
    await ds.stopDictating();
    b.un();
    return { got: b.got, draft: drafts.get("b") ?? null, n: transcribes.length };
  });
  check(r.got.length === 1 && r.got[0] === "hello world" && r.draft === null && r.n === 2, "a box on screen gets the words directly", r);

  // 3. One dictation at a time.
  r = await p.evaluate(async () => {
    const asks = micAsks;
    await ds.startDictating(target("c"));
    await sleep(300);
    toasts.length = 0;
    await ds.startDictating(target("d"));
    const during = { ...state(), asks: micAsks - asks, live: live(), toasts: [...toasts] };
    ds.cancelDictating();
    return { during, after: { ...state(), live: live(), n: transcribes.length, draft: drafts.get("c") ?? null } };
  });
  check(r.during.target === "c" && r.during.asks === 1 && r.during.live === 1, "a second start does not open another microphone", r.during);
  check(r.during.toasts.some((t) => /Already dictating into Agent c/.test(t[1])), "a second start says one is running", r.during.toasts);
  check(r.after.phase === "idle" && r.after.live === 0 && r.after.n === 2 && r.after.draft === null, "Throw away releases the microphone and transcribes nothing", r.after);

  // 4. Stopped while the microphone permission is still pending.
  r = await p.evaluate(async () => {
    micDelay = 1200;
    const started = ds.startDictating(target("e"));
    await sleep(200);
    const pending = state();
    await ds.stopDictating();
    const stopped = state();
    await started;
    await sleep(200);
    micDelay = 0;
    return { pending, stopped, live: live(), n: transcribes.length };
  });
  check(r.pending.phase === "starting" && r.pending.target === "e", "while the permission is pending the bar says so", r.pending);
  check(r.stopped.phase === "idle", "Stop while pending ends it at once", r.stopped);
  check(r.live === 0 && r.n === 2, "the microphone that arrives late is released and nothing is transcribed", r);

  // 5. The microphone goes away: what was heard so far is kept.
  r = await p.evaluate(async () => {
    toasts.length = 0;
    await ds.startDictating(target("f"));
    await sleep(800);
    mics.at(-1).getAudioTracks()[0].dispatchEvent(new Event("ended"));
    await idle();
    return { ...state(), n: transcribes.length, draft: drafts.get("f") ?? null, live: live(), toasts: [...toasts] };
  });
  check(r.phase === "idle" && r.n === 3 && r.draft === "hello world", "a microphone that went away keeps what was heard", r);
  check(r.toasts.some((t) => /microphone stopped/.test(t[1])), "the owner is told the microphone stopped", r.toasts);

  // 6. Closing or reloading the window asks while a dictation runs, not otherwise.
  r = await p.evaluate(async () => {
    const ask = () => {
      const e = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(e);
      return e.defaultPrevented;
    };
    await ds.startDictating(target("g"));
    const running = ask();
    ds.cancelDictating();
    return { running, after: ask() };
  });
  check(r.running && !r.after, "closing the window asks only while a dictation runs", r);
  await ctx.close();
} finally {
  await browser.close();
  server.close();
}
console.log(failures ? `${failures} check(s) failed` : "all checks passed");
process.exit(failures ? 1 : 0);
