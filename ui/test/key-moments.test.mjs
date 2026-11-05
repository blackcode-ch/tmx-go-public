import test from "node:test";
import assert from "node:assert/strict";
import { keyMoments, lineAt, MAX_MOMENTS, place, POINTING, segmentStarts } from "../src/lib/key-moments.ts";
import { greyChange, toGrey } from "../src/lib/rec-frames.ts";

test("key moments: the start, every screen change, what was pointed at; 8 s apart, a spoken one first", () => {
  const frames = [
    { ms: 0 },
    { ms: 30_000 },
    { ms: 41_000, key: true },
    { ms: 45_000, key: true }, // within 8 s of 41 s: dropped
    { ms: 60_000 },
    { ms: 63_500, key: true },
    { ms: 90_000 },
  ];
  const lines = [
    { ms: 5_000, text: "Hello, this is the review." },
    { ms: 58_000, text: "Look at this button here" }, // the still just after: 60 s, which beats the change at 63.5 s
    { ms: 200_000, text: "regardez ici" }, // nothing within 20 s
  ];
  assert.deepEqual(keyMoments(frames, lines), [
    { ms: 0, why: "start" },
    { ms: 41_000, why: "screen changed" },
    { ms: 60_000, why: "said", said: "Look at this button here" },
  ]);
  assert.deepEqual(keyMoments([]), []);
  const many = Array.from({ length: 100 }, (_, i) => ({ ms: i * 10_000, key: true }));
  assert.equal(keyMoments(many).length, MAX_MOMENTS);
});

test("pointing words, in the owner's languages, as whole words", () => {
  for (const s of ["look", "you can SEE it", "schau mal", "regardez", "voilà", "c'est ici", "ecco", "mira aquí"]) assert.ok(POINTING.test(s), s);
  for (const s of ["lookup table", "seen", "there is a theme", "quiet"]) assert.ok(!POINTING.test(s), s);
});

test("places, starts and the line being heard", () => {
  const segs = [
    { n: 1, duration_ms: 300_000 },
    { n: 2, duration_ms: 300_000 },
    { n: 3, duration_ms: 12_000 },
  ];
  assert.deepEqual(place(segs, 0), { n: 1, ms: 0 });
  assert.deepEqual(place(segs, 301_000), { n: 2, ms: 1000 });
  assert.deepEqual(place(segs, 999_000), { n: 3, ms: 399_000 });
  assert.deepEqual(
    [...segmentStarts(segs)],
    [
      [1, 0],
      [2, 300_000],
      [3, 600_000],
    ],
  );
  const lines = [
    { from_ms: 1000, to_ms: 3000 },
    { from_ms: 3000, to_ms: 6000 },
    { from_ms: 20_000, to_ms: 22_000 },
  ];
  assert.equal(lineAt(lines, 500), -1);
  assert.equal(lineAt(lines, 1000), 0);
  assert.equal(lineAt(lines, 3000), 1);
  assert.equal(lineAt(lines, 7000), 1); // a short pause keeps the line
  assert.equal(lineAt(lines, 10_000), -1);
  assert.equal(lineAt(lines, 21_000), 2);
  assert.equal(lineAt([], 1), -1);
});

test("screen change: the share of grey cells that moved by more than 24 levels", () => {
  const a = toGrey(new Uint8Array(4 * 100).fill(100));
  const px = new Uint8Array(4 * 100).fill(100);
  for (let i = 0; i < 5 * 4; i++) px[i] = 200; // 5 of 100 cells much brighter
  assert.equal(greyChange(a, toGrey(px)), 0.05);
  assert.equal(greyChange(a, a), 0);
  assert.equal(greyChange(a, new Uint8Array(3)), 0);
});
