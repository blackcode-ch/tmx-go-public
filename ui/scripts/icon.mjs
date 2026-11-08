// Canonical Living cell sources; deterministic PNGs without launching a browser.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { Resvg } from "@resvg/resvg-js";

const ui = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const repo = path.resolve(ui, "..");
function render(source, size, out) {
  const svg = fs.readFileSync(path.join(ui, "public", source), "utf8");
  fs.writeFileSync(path.join(repo, out), new Resvg(svg, { fitTo: { mode: "width", value: size } }).render().asPng());
  console.log(`wrote ${out} (${size}x${size})`);
}
for (const size of [192, 512]) {
  render("living-cell.svg", size, `ui/public/icon-${size}.png`);
  render("living-cell-maskable.svg", size, `ui/public/icon-maskable-${size}.png`);
}
render("living-cell.svg", 180, "ui/public/apple-touch-icon.png");
render("living-cell.svg", 1024, "build/appicon.png");
