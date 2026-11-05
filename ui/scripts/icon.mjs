// Renders the desktop app icon, build/appicon.png (1024x1024; Wails builds the macOS .icns and
// the Windows .ico from it), from its vector source build/appicon.svg: the Pocket dragon (Tiamat),
// the Python dashboard's Tiamatrix icon (the owner, 3 Nov: the grid is out). The UI's favicon and
// PWA icons are NOT rendered here any more: they are that design's own files, taken from git
// (terminal-matrix tmx/design/tiamatrix-green and the dragon set at de5ba01c^), byte for byte.
// Uses Playwright's bundled Chromium headless: Linux CI only (e2e.yml's shots job, -f icon=true),
// never on the owner's PCs.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const ui = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const repo = path.resolve(ui, "..");
const src = path.join(repo, "build", "appicon.svg");
const svg = fs.readFileSync(src, "utf8");

const browser = await chromium.launch({ headless: true, args: ["--disable-features=WebAuthenticationUseNativeWinApi"] });
try {
  const render = async (size, out, background) => {
    const page = await browser.newPage({ viewport: { width: size, height: size }, deviceScaleFactor: 1 });
    const body = svg.replace(/<svg\b/, `<svg style="display:block;width:${size}px;height:${size}px"`);
    await page.setContent(
      `<!doctype html><html><head><style>html,body{margin:0;padding:0;background:${background}}</style></head><body>${body}</body></html>`,
    );
    await page.screenshot({ path: out, omitBackground: background === "transparent", clip: { x: 0, y: 0, width: size, height: size } });
    await page.close();
    console.log(`wrote ${path.relative(repo, out)} (${size}x${size})`);
  };
  await render(1024, path.join(repo, "build", "appicon.png"), "transparent");
} finally {
  await browser.close();
}
