// Screenshots of the resource-control screens (mock data): node scripts/shots-resources.mjs [filter]
// Same harness as shots.mjs.
// Serves ui/dist (only) on 127.0.0.1, drives Playwright's bundled Chromium headless, writes
// PNGs to ui/screenshots/ (git-ignored). Run `pnpm build` first. On a shared machine wrap it:
//   tmx browser-slot -- node scripts/shots.mjs
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const dist = path.join(root, "dist");
const out = path.join(root, "screenshots");
fs.mkdirSync(out, { recursive: true });
const filter = process.argv[2] ?? "";

const TYPES = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".woff2": "font/woff2", ".svg": "image/svg+xml" };
const server = http.createServer((req, res) => {
  const u = new URL(req.url, "http://x");
  let p = path.normalize(path.join(dist, decodeURIComponent(u.pathname)));
  if (!p.startsWith(dist)) return res.writeHead(403).end();
  if (fs.existsSync(p) && fs.statSync(p).isDirectory()) p = path.join(p, "index.html");
  if (!fs.existsSync(p)) return res.writeHead(404).end();
  res.writeHead(200, { "Content-Type": TYPES[path.extname(p)] ?? "application/octet-stream" });
  fs.createReadStream(p).pipe(res);
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const base = `http://127.0.0.1:${server.address().port}/`;

const SIZES = { wide: { width: 1440, height: 900 }, small: { width: 1100, height: 720 }, phone: { width: 390, height: 844 } };

// name, url (query + hash), sizes, setup(page)
const SHOTS = [
  ["res-overview", "?mock=still#/", ["wide", "phone"], null, ["light", "dark"]],
  ["res-overview-head-paused", "?mock=head-paused#/", ["wide"]],
  ["res-agent-paused", "?mock=still#/agents/wk4p6r8t2u", ["wide"], null, ["light", "dark"]],
  ["res-agent-queued", "?mock=still#/agents/wk5q7s9u3v", ["wide"]],
  ["res-agent-details", "?mock=still#/agents/wk2m4n6p8q/details", ["wide"]],
  ["res-inbox", "?mock=still#/inbox", ["wide"]],
  [
    "res-settings",
    "?mock=still#/settings",
    ["wide"],
    async (p) => {
      await p.evaluate(() => document.getElementById("resources")?.scrollIntoView({ block: "start" }));
      await p.waitForTimeout(300);
    },
  ],
];

const browser = await chromium.launch({
  headless: true,
  args: ["--disable-features=WebAuthenticationUseNativeWinApi"],
});
const errors = [];
try {
  for (const theme of ["light", "dark"]) {
    // A handful of shots: everything light, a dark check where the 5th field asks for it.
    for (const [name, url, sizes, setup, themes = ["light"]] of SHOTS) {
      if (!themes.includes(theme)) continue;
      for (const size of sizes) {
        const id = `${name}-${size}-${theme}`;
        if (filter && !id.includes(filter)) continue;
        const ctx = await browser.newContext({
          viewport: SIZES[size],
          deviceScaleFactor: 2,
          colorScheme: theme,
          isMobile: size === "phone",
          hasTouch: size === "phone",
        });
        const page = await ctx.newPage();
        const cdp = await ctx.newCDPSession(page);
        await cdp.send("WebAuthn.enable");
        await cdp.send("WebAuthn.addVirtualAuthenticator", {
          options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true },
        });
        page.on("pageerror", (e) => errors.push(`${id}: ${e.message}`));
        page.on("console", (m) => m.type() === "error" && errors.push(`${id}: console: ${m.text()}`));
        await page.goto(base + url);
        await page.waitForSelector("main", { timeout: 10000 });
        await page.waitForTimeout(900);
        if (setup) await setup(page);
        await page.screenshot({ path: path.join(out, `${id}.png`) });
        console.log("shot", id);
        await ctx.close();
      }
    }
  }
} finally {
  await browser.close();
  server.close();
}
if (errors.length) {
  console.log("\nPage errors:\n" + errors.join("\n"));
  process.exitCode = 1;
}
