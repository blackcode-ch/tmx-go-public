import test from "node:test";
import assert from "node:assert/strict";
import { turnResult } from "../src/lib/turn-result.ts";

test("legacy interruption, abort and supersession render without owner attribution", () => {
  for (const subtype of ["interrupted", "aborted", "superseded"]) {
    assert.deepEqual(turnResult({ meta: { subtype }, is_error: true }), { label: "Ended", failed: false });
  }
  assert.deepEqual(turnResult({ meta: { subtype: "error_during_execution" }, is_error: true, text: "[Request interrupted by user]" }), {
    label: "Ended", failed: false,
  });
  assert.deepEqual(turnResult({ meta: { subtype: "interrupted", owner_interrupt: true } }), {
    label: "Interrupted", failed: false,
  });
});

test("execution errors keep error styling", () => {
  assert.deepEqual(turnResult({ meta: { subtype: "error_during_execution" }, is_error: true }), {
    label: "error during execution", failed: true,
  });
  assert.deepEqual(turnResult({ meta: { subtype: "success" }, is_error: true }), {
    label: "Ended with an error", failed: true,
  });
});
