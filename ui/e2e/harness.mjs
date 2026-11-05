// Browser e2e harness (recorder-fix-o for tmx-lead, 3 Nov 2026): the owner's screens in a real browser, the way he
// uses them. Linux CI only (.github/workflows/e2e.yml): a browser never runs on the owner's PCs.
//
// Each test gets a THROWAWAY daemon: the gatesim build (owner approvals without an OS prompt, only for a fresh
// opted-in home), its own TMXG_HOME and port, voice transcription off (no model download in CI), and summaries
// from a fake local model (a loopback HTTP server answering like Ollama). The browser is Playwright's bundled
// Chromium with fake camera/microphone, a drawn screen for getDisplayMedia, and a CDP virtual authenticator added
// before the first page (never the OS authenticator). A "phone" reaches the daemon the way a phone does, through a
// trusted HTTPS proxy: here a loopback TLS proxy on https://tmx.localhost:<port> (*.localhost resolves to loopback
// in Chromium) that adds X-Forwarded-Proto: https, listed in the daemon's api.trusted_hosts.
import { spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import http from "node:http";
import https from "node:https";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { chromium } from "playwright";

// Linux CI only, enforced (go-reviewer-o / tmx-lead, 3 Nov): this harness starts real browsers and a gatesim
// daemon, and the job installs a throwaway CA; none of that may ever run on the owner's PCs.
if (process.platform !== "linux" || process.env.CI !== "true") {
  throw new Error("ui/e2e runs in Linux CI only (CI=true on linux): never a real browser or a gatesim daemon on the owner's PCs");
}

export const BIN = process.env.TMXG_E2E_BIN || path.resolve(import.meta.dirname, "../../bin/tmx-gatesim");

const freePort = () => new Promise((resolve, reject) => {
  const s = net.createServer().listen(0, "127.0.0.1", () => { const p = s.address().port; s.close(() => resolve(p)); });
  s.on("error", reject);
});

const sleep = ms => new Promise(r => setTimeout(r, ms));

/** A fake local model: answers /api/generate like Ollama with a summary in the JSON the daemon asks for. */
async function fakeOllama() {
  const asks = [];
  const srv = http.createServer((req, res) => {
    let body = "";
    req.on("data", c => (body += c));
    req.on("end", () => {
      if (req.url !== "/api/generate") { res.writeHead(404).end(); return; }
      const b = JSON.parse(body || "{}");
      asks.push({ model: b.model, images: (b.images || []).length, prompt: String(b.prompt || "").slice(0, 2000) });
      const summary = { summary: "E2E: the owner walked through the screen while talking to the camera.",
        key_points: ["The screen and the camera were recorded"], decisions: [], action_items: [] };
      res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ response: JSON.stringify(summary) }));
    });
  });
  await new Promise(r => srv.listen(0, "127.0.0.1", r));
  return { url: `http://127.0.0.1:${srv.address().port}`, asks, close: () => new Promise(r => srv.close(r)) };
}

// The phone's HTTPS: a certificate for tmx.localhost from a throwaway CA the CI job made and trusted in
// Chromium's NSS store (TMXG_E2E_TLS_DIR with cert.pem/key.pem); the dashboard offers pairing only over https,
// and its __Host- cookies want a secure page.
function tlsOptions() {
  const dir = process.env.TMXG_E2E_TLS_DIR;
  if (!dir) throw new Error("TMXG_E2E_TLS_DIR is not set (the CI job makes a throwaway CA and certificate for tmx.localhost)");
  return { cert: readFileSync(path.join(dir, "cert.pem")), key: readFileSync(path.join(dir, "key.pem")) };
}

/** The trusted HTTPS proxy a phone goes through (see the header). */
async function phoneProxy(daemonPort) {
  const port = await freePort();
  const srv = https.createServer(tlsOptions(), (req, res) => {
    const up = http.request({ host: "127.0.0.1", port: daemonPort, method: req.method, path: req.url,
      headers: { ...req.headers, "x-forwarded-proto": "https" } }, r => { res.writeHead(r.statusCode, r.headers); r.pipe(res); });
    up.on("error", () => { res.writeHead(502).end(); });
    req.pipe(up);
  });
  srv.on("upgrade", (req, sock, head) => {     // the dashboard's websocket
    const up = net.connect(daemonPort, "127.0.0.1", () => {
      const lines = [`${req.method} ${req.url} HTTP/1.1`];
      for (let i = 0; i < req.rawHeaders.length; i += 2) lines.push(`${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}`);
      lines.push("X-Forwarded-Proto: https", "", "");
      up.write(lines.join("\r\n")); up.write(head); sock.pipe(up).pipe(sock);
    });
    up.on("error", () => sock.destroy());
  });
  await new Promise(r => srv.listen(port, "127.0.0.1", r));
  return { port, origin: `https://tmx.localhost:${port}`, host: `tmx.localhost:${port}`, close: () => new Promise(r => srv.close(r)) };
}

/** Starts a throwaway daemon; call stop() at the end. */
export async function startDaemon({ phone = false, config = {} } = {}) {
  const home = mkdtempSync(path.join(os.tmpdir(), "tmxg-e2e-"));
  const port = await freePort();
  const model = await fakeOllama();
  const proxy = phone ? await phoneProxy(port) : null;
  mkdirSync(home, { recursive: true });
  writeFileSync(path.join(home, "config.json"), JSON.stringify({
    port,
    voice: { auto_transcribe: false },
    summaries: { local_url: model.url },
    ...(proxy ? { api: { trusted_hosts: [proxy.host] } } : {}),
    ...config,
  }, null, 1));
  const log = [];
  const proc = spawn(BIN, ["daemon"], {
    env: { ...process.env, TMXG_HOME: home, TMXG_GATESIM: home, TMXG_PORT: String(port) },
    stdio: ["ignore", "pipe", "pipe"],
  });
  for (const s of [proc.stdout, proc.stderr]) s.on("data", d => { log.push(String(d)); if (log.length > 400) log.shift(); });
  const url = `http://127.0.0.1:${port}`;
  for (let i = 0; ; i++) {
    try { if ((await fetch(url + "/api/health")).ok) break; } catch { /* not up yet */ }
    if (proc.exitCode !== null || i > 300) throw new Error("the daemon did not start:\n" + log.join(""));
    await sleep(100);
  }
  const token = readFileSync(path.join(home, "secrets", "owner-token"), "utf8").trim();
  const api = async (method, p, body) => {
    const r = await fetch(url + p, { method, headers: { Authorization: "Bearer " + token, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body) });
    const text = await r.text();
    if (!r.ok) throw new Error(`${method} ${p}: ${r.status} ${text}`);
    return text ? JSON.parse(text) : null;
  };
  return {
    home, port, url, token, model, proxy, api, log,
    ownerURL: (route = "/") => `${url}/#token=${encodeURIComponent(token)}`,
    async stop() {
      proc.kill("SIGTERM");
      await Promise.race([new Promise(r => proc.once("exit", r)), sleep(5000)]);
      if (proc.exitCode === null) proc.kill("SIGKILL");
      await model.close();
      await proxy?.close();
      rmSync(home, { recursive: true, force: true });
    },
  };
}

/** Bundled Chromium with fake camera/mic; never the OS authenticator (7 Oct lockout). */
export async function launchBrowser() {
  return chromium.launch({
    headless: true,
    args: [
      "--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream", "--autoplay-policy=no-user-gesture-required",
      "--disable-features=WebAuthenticationUseNativeWinApi",
    ],
  });
}

// A drawn "screen" for getDisplayMedia: headless Chromium has no screen picker. Camera and mic stay Chromium's
// own fake devices. It shows slides: a new one every 10 s (more than KEY_GAP_MS apart), each a different
// lightness, so the grey diff (rec-frames.ts greyChange: > 24 levels) sees a key moment at every cut. A hue
// sweep alone barely moves the grey levels, which is why no key moments were found before.
const FAKE_SCREEN = `(() => {
  const md = navigator.mediaDevices; if (!md) return;
  md.getDisplayMedia = async () => {
    const c = document.createElement('canvas'); c.width = 1280; c.height = 720; const g = c.getContext('2d'); let f = 0;
    const light = [20, 70, 35, 85];
    setInterval(() => { const s = Math.floor(f++ / 100);
      g.fillStyle = 'hsl(' + (s * 75 % 360) + ',50%,' + light[s % light.length] + '%)'; g.fillRect(0, 0, 1280, 720);
      g.fillStyle = light[s % light.length] > 50 ? '#000' : '#fff'; g.font = '48px sans-serif'; g.fillText('e2e slide ' + s, 60, 360); }, 100);
    return c.captureStream(10);
  };
})();`;

/** A page in a new context: phone size optional. */
export async function openPage(browser, { phone = false, origin } = {}) {
  const context = await browser.newContext(phone
    ? { viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true }
    : { viewport: { width: 1280, height: 900 } });
  if (origin) await context.grantPermissions(["camera", "microphone"], { origin });
  await context.addInitScript(FAKE_SCREEN);
  const page = await context.newPage();
  const cdp = await context.newCDPSession(page);
  // No virtual authenticator: the served UI never uses WebAuthn (the owner PIN replaced passkeys).
  const errors = [];
  page.on("pageerror", e => errors.push(String(e)));
  return { context, page, cdp, errors };
}

/** Opens the dashboard as the owner: the page takes #token=… once (after it checked for the desktop bridge) and
 *  strips it; only then may a test change the route, or the token would be overwritten before it was read. */
export async function signIn(page, d) {
  await page.goto(d.ownerURL());
  await page.waitForFunction(() => !location.hash.includes("token="), null, { timeout: 15_000 });
}

/** Counts the visible buttons whose accessible text matches rx. */
export async function visibleButtons(page, rx) {
  const out = [];
  for (const b of await page.getByRole("button").all()) {
    if (!(await b.isVisible())) continue;
    const label = ((await b.getAttribute("aria-label")) || "") + " " + (await b.innerText());
    if (rx.test(label)) out.push(label.trim());
  }
  return out;
}
