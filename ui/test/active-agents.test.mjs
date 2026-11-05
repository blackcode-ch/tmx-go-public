import test from "node:test";
import assert from "node:assert/strict";
import { agentVisible } from "../src/lib/active-agents.ts";

test("active list shows working and recently idle; Show all restores history", () => {
  const now = 10_000_000;
  for (const state of ["working", "starting", "waiting", "queued", "paused"]) {
    assert.equal(agentVisible({ state }, false, now), true);
  }
  assert.equal(agentVisible({ state: "idle", last_activity_at: now - 1000 }, false, now), true);
  assert.equal(agentVisible({ state: "idle", last_activity_at: now - 7_200_000 }, false, now), false);
  for (const state of ["stopped", "exited", "error"]) {
    assert.equal(agentVisible({ state }, false, now), false);
    assert.equal(agentVisible({ state }, true, now), true);
  }
  assert.equal(agentVisible({ state: "idle", last_activity_at: now, shelved_at: now }, false, now), false);
  assert.equal(agentVisible({ state: "stopped", shelved_at: now }, true, now), true);
});
