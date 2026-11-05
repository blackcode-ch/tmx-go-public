// Home as Daily (src/lib/daily.ts): the greeting by the hour and the one-line summary.
import test from "node:test";
import assert from "node:assert/strict";
import { dailyLine, greeting } from "../src/lib/daily.ts";

test("greeting by the time of day", () => {
  assert.equal(greeting(7), "Good morning.");
  assert.equal(greeting(12), "Good afternoon.");
  assert.equal(greeting(17), "Good afternoon.");
  assert.equal(greeting(18), "Good evening.");
  assert.equal(greeting(2), "Good evening.");
});

test("one plain line: what moves, what waits", () => {
  assert.equal(dailyLine(4, 1), "Four things move, one waits for you.");
  assert.equal(dailyLine(1, 3), "One thing moves, three wait for you.");
  assert.equal(dailyLine(2, 0), "Two things move, nothing waits for you.");
  assert.equal(dailyLine(0, 0), "All quiet: nothing waits for you.");
  assert.equal(dailyLine(0, 2), "Two wait for you.");
  assert.equal(dailyLine(12, 14), "12 things move, 14 wait for you.");
});
