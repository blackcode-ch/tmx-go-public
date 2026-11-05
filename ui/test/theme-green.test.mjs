// The Tiamatrix green is Go's default (#235, 3 Nov): the Python dashboard's palette and the Pocket
// dragon's (terminal-matrix tmx/design/tiamatrix-green/palette.json), light and dark.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const css = readFileSync(new URL("../src/index.css", import.meta.url), "utf8");
const block = (sel) => css.slice(css.indexOf(sel + " {"), css.indexOf("\n}", css.indexOf(sel + " {")));

test("light: the green palette's bg, panel, text and accent", () => {
  const b = block(":root");
  for (const [k, v] of [["--background", "#f1f5ed"], ["--card", "#fbfcf8"], ["--foreground", "#202a25"], ["--primary", "#356348"], ["--brand", "#356348"]]) {
    assert.match(b, new RegExp(`\n  ${k}: ${v};`), `${k} should be ${v}`);
  }
});

test("dark: the green palette's bg, panel, text and accent", () => {
  const b = block(".dark");
  for (const [k, v] of [["--background", "#151e19"], ["--card", "#202a25"], ["--foreground", "#eaf1e8"], ["--primary", "#b7d3a3"], ["--brand", "#b7d3a3"]]) {
    assert.match(b, new RegExp(`\n  ${k}: ${v};`), `${k} should be ${v}`);
  }
});
