// asList (src/lib/as-list.ts): the process list on agent details crashed on a phone with
// "n.map is not a function" (3 Nov) when a list arrived as something else. Every shape that
// isn't an array is an empty list; an array passes through untouched.
import test from "node:test";
import assert from "node:assert/strict";
import { asList } from "../src/lib/as-list.ts";

test("asList: null, an object, a string, a number, undefined are all an empty list", () => {
  for (const bad of [null, undefined, {}, { processes: [] }, { id: "x" }, "warning", 3, true]) {
    assert.deepEqual(asList(bad), [], JSON.stringify(bad));
  }
});

test("asList: an array is returned as it is, and what a page does with it works", () => {
  const rows = [{ id: "p1", promoted: true }, { id: "p2", promoted: false }];
  assert.equal(asList(rows), rows);
  assert.deepEqual(asList(rows).filter((r) => r.promoted).map((r) => r.id), ["p1"]);
  assert.deepEqual(asList(null).filter((r) => r.promoted).map((r) => r.id), []);
});
