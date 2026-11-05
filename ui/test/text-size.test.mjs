// Text size (src/lib/text-size.ts; the owner, 3 Nov: "everything is too tiny"): Normal is 125% of
// the old size, Large and Larger more; anything unknown or missing is Normal.
import test from "node:test";
import assert from "node:assert/strict";
import { TEXT_SIZES, textSizePercent } from "../src/lib/text-size.ts";

test("Normal is the new default, at least 125% of the old size; Large and Larger grow from there", () => {
  assert.deepEqual(TEXT_SIZES.map((t) => t.id), ["normal", "large", "larger"]);
  assert.ok(textSizePercent("normal") >= 125);
  assert.ok(textSizePercent("large") > textSizePercent("normal"));
  assert.ok(textSizePercent("larger") > textSizePercent("large"));
});

test("an unknown or missing size is Normal", () => {
  for (const s of [null, undefined, "", "huge", "LARGER"]) assert.equal(textSizePercent(s), textSizePercent("normal"));
});
