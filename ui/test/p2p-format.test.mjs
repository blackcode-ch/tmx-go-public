import test from "node:test";
import assert from "node:assert/strict";
import { onOff, reachabilityLabel, savedVsLiveBanner, shouldPollP2P } from "../src/lib/p2p-format.ts";

test("reachabilityLabel: off beats whatever reachability says", () => {
  assert.equal(reachabilityLabel({ enabled: false, reachability: "direct" }), "Off");
  assert.equal(reachabilityLabel({ enabled: false, reachability: "offline" }), "Off");
});

test("reachabilityLabel: direct, relayed and offline while enabled", () => {
  assert.equal(reachabilityLabel({ enabled: true, reachability: "direct" }), "Connected (direct)");
  assert.equal(reachabilityLabel({ enabled: true, reachability: "relayed" }), "Connected (through a relay)");
  assert.equal(reachabilityLabel({ enabled: true, reachability: "offline" }), "Not reachable yet");
});

test("onOff", () => {
  assert.equal(onOff(true), "on");
  assert.equal(onOff(false), "off");
});

test("savedVsLiveBanner: nothing to say once saved and live agree", () => {
  assert.equal(savedVsLiveBanner({ enabled: true, saved_enabled: true, restart_to_apply: false }), null);
  assert.equal(savedVsLiveBanner({ enabled: false, saved_enabled: false, restart_to_apply: false }), null);
});

test("savedVsLiveBanner: names both the saved and the live state while they differ", () => {
  assert.equal(
    savedVsLiveBanner({ enabled: false, saved_enabled: true, restart_to_apply: true }),
    "Saved: on — NOT live until restart (currently off).",
  );
  assert.equal(
    savedVsLiveBanner({ enabled: true, saved_enabled: false, restart_to_apply: true }),
    "Saved: off — NOT live until restart (currently on).",
  );
});

test("shouldPollP2P: only while a restart is pending, and never on no status yet", () => {
  assert.equal(shouldPollP2P(null), false);
  assert.equal(shouldPollP2P({ restart_to_apply: false }), false);
  assert.equal(shouldPollP2P({ restart_to_apply: true }), true);
});
