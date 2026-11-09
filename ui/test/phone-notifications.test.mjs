import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { phoneNotificationPrompt } from "../src/lib/phone-notifications.ts";

const device = { supported: true, ready: true, enabled: false, dismissed: false, ios: false };
test("banner copy has no question-mark replacement artefacts", () => {
  const source = readFileSync(new URL("../src/components/phone-notifications.tsx", import.meta.url), "utf8");
  const homeScreen = source.match(/>(iPhone[^<]*)<\/p>/)?.[1];
  const enabling = source.match(/"(Enabling[^"\n]*)"/)?.[1];
  assert.equal(typeof homeScreen, "string");
  assert.equal(typeof enabling, "string");
  for (const copy of [homeScreen, enabling]) assert.doesNotMatch(copy, /[?\uFFFD]/);
});
test("show only after checking this device's subscription", () => {
  assert.equal(phoneNotificationPrompt(device), "enable");
  assert.equal(phoneNotificationPrompt({ ...device, ready: false }), "hidden");
  assert.equal(phoneNotificationPrompt({ ...device, enabled: true }), "hidden");
});
test("Later hides this open; a fresh open prompts again", () => {
  assert.equal(phoneNotificationPrompt({ ...device, dismissed: true }), "hidden");
  assert.equal(phoneNotificationPrompt({ ...device, dismissed: false }), "enable");
});
test("unsupported browsers never offer Enable; iPhone gets the Home Screen hint", () => {
  assert.equal(phoneNotificationPrompt({ ...device, supported: false }), "hidden");
  assert.equal(phoneNotificationPrompt({ ...device, supported: false, ios: true }), "home-screen");
  assert.equal(phoneNotificationPrompt({ ...device, ios: true }), "enable");
});
