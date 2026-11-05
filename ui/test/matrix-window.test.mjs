// The Matrix list's load budget (src/lib/matrix-window.ts, M3): with 5,000 threads only the
// rows in view are rendered, at every scroll position, and working that out is cheap enough to
// run on every scroll event on a phone.
import test from "node:test";
import assert from "node:assert/strict";
import { OVERSCAN, ROW_H, visibleRange } from "../src/lib/matrix-window.ts";

const N = 5000;
const PHONE = 740; // px of list on a phone
const DESKTOP = 1300;

test("visibleRange: a phone renders a screenful, not 5,000 rows, from top to bottom", () => {
  const most = Math.ceil(PHONE / ROW_H) + 2 * OVERSCAN + 1;
  for (let top = 0; top <= N * ROW_H; top += 97) {
    const { first, last } = visibleRange(top, PHONE, N);
    assert.ok(first >= 0 && last <= N && first <= last, `range ${first}-${last} at ${top}`);
    assert.ok(last - first <= most, `${last - first} rows at ${top} (most ${most})`);
    // the rows actually in view are always among those rendered
    const inView = Math.floor(top / ROW_H);
    if (inView < N) assert.ok(first <= inView && inView < last, `row ${inView} not rendered at ${top}`);
  }
});

test("visibleRange: edges (empty list, past the end, a list shorter than the view)", () => {
  assert.deepEqual(visibleRange(0, PHONE, 0), { first: 0, last: 0 });
  assert.deepEqual(visibleRange(10 * N * ROW_H, PHONE, N), { first: N, last: N });
  assert.deepEqual(visibleRange(0, DESKTOP, 3), { first: 0, last: 3 });
});

test("visibleRange: a full scroll through 5,000 rows stays far under a frame per step", () => {
  const rows = Array.from({ length: N }, (_, i) => ({ id: `i-${i}`, title: `Request ${i}` }));
  const t0 = performance.now();
  let rendered = 0;
  for (let top = 0; top <= N * ROW_H; top += 16) {
    const { first, last } = visibleRange(top, DESKTOP, N);
    rendered += rows.slice(first, last).length; // what the list maps to elements
  }
  const steps = Math.ceil((N * ROW_H) / 16);
  const perStep = (performance.now() - t0) / steps;
  assert.ok(rendered > 0);
  assert.ok(perStep < 0.5, `${perStep.toFixed(4)} ms per scroll step (budget 0.5 ms; a frame is 16 ms)`);
});
