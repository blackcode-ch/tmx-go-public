import test from "node:test";
import assert from "node:assert/strict";
import {
  connectedSummary,
  fullestLimit,
  harnessMark,
  isLink,
  limitTone,
  normalizeAccounts,
  resetLabel,
  toneWords,
  untilWords,
  usedPct,
} from "../src/lib/accounts-format.ts";

const acc = (over) => ({
  harness: "claude",
  label: "Claude",
  installed: true,
  connected: true,
  account: null,
  limits: [],
  limits_note: "",
  checked_at: 0,
  refreshing: false,
  error: "",
  login: { state: "idle", message: "" },
  install: "",
  ...over,
});

test("quota tones: green under 70, amber from 70, red from 90, with words for each", () => {
  assert.equal(limitTone(0), "ok");
  assert.equal(limitTone(69.4), "ok");
  assert.equal(limitTone(70), "warn");
  assert.equal(limitTone(89), "warn");
  assert.equal(limitTone(90), "hot");
  assert.equal(limitTone(140), "hot");
  assert.equal(toneWords(50), "");
  assert.equal(toneWords(72), "Getting close");
  assert.equal(toneWords(91), "Almost used up");
  assert.equal(toneWords(100), "Used up");
  assert.equal(usedPct(-3), 0);
  assert.equal(usedPct(100.4), 100);
  assert.equal(usedPct(Number.NaN), 0);
});

test("resets: a countdown from resets_at, the harness's words when it is 0", () => {
  const now = 1_000_000_000_000;
  assert.equal(resetLabel({ resets_at: now + 3 * 3600_000 + 12 * 60_000, resets_text: "" }, now), "Resets in 3h 12m");
  assert.equal(resetLabel({ resets_at: now + 47 * 60_000, resets_text: "ignored" }, now), "Resets in 47m");
  assert.equal(resetLabel({ resets_at: 0, resets_text: "Nov 3 at 6am" }, now), "Resets Nov 3 at 6am");
  assert.equal(resetLabel({ resets_at: 0, resets_text: "" }, now), "");
  assert.equal(resetLabel({ resets_at: now - 1, resets_text: "" }, now), "Resets any moment");
  assert.equal(untilWords(30_000), "under a minute");
  assert.equal(untilWords(2 * 86400_000 + 5 * 3600_000), "2d 5h");
  assert.equal(untilWords(2 * 3600_000), "2h");
});

test("the summary counts connected accounts and says when nothing is installed", () => {
  assert.equal(connectedSummary([acc({}), acc({ harness: "codex" }), acc({ harness: "antigravity", connected: false })]), "2 of 3 AI accounts connected");
  assert.equal(connectedSummary([acc({ installed: false, connected: false })]), "No AI tools installed yet");
  assert.equal(connectedSummary([]), "No AI tools found");
  assert.equal(connectedSummary([acc({})]), "1 of 1 AI account connected");
});

test("the fullest limit counts connected accounts only", () => {
  const l = (id, p) => ({ id, label: id, used_percent: p, resets_at: 0, resets_text: "" });
  const list = [
    acc({ limits: [l("session", 8), l("week", 54)] }),
    acc({ harness: "codex", limits: [l("5h", 91)] }),
    acc({ harness: "antigravity", connected: false, limits: [l("stale", 99)] }),
  ];
  assert.equal(fullestLimit(list)?.limit.id, "5h");
  assert.equal(fullestLimit([]), null);
});

test("marks, links, and partial payloads", () => {
  assert.equal(harnessMark("claude"), "Cl");
  assert.equal(harnessMark("antigravity"), "Ag");
  assert.equal(harnessMark("opencode", "opencode"), "Op");
  assert.ok(isLink("https://antigravity.google/download"));
  assert.ok(!isLink("curl -fsSL https://claude.ai/install.sh | bash"));
  const p = normalizeAccounts({ accounts: [{ harness: "codex", label: "Codex", installed: true, connected: false, checked_at: 0, refreshing: false, limits: null }] });
  assert.deepEqual(p.accounts[0].limits, []);
  assert.deepEqual(p.accounts[0].login, { state: "idle", message: "" });
  assert.equal(p.updated_at, 0);
});
