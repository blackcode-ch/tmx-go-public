// The Chat / Terminal / Details switch of an agent under the classic UI profile
// (src/lib/agent-view.ts; the owner #7, 17 Nov: Python's toggle): a click only changes the view,
// the last of Chat / Terminal is remembered per agent, a new agent opens on Chat, and nothing
// in the agent page changes the agent's mode by looking.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  AGENT_VIEWS,
  defaultAgentView,
  isAgentView,
  parseAgentViews,
  rememberAgentView,
  rememberedAgentView,
  resolveAgentView,
  withAgentView,
} from "../src/lib/agent-view.ts";

const src = (p) => readFileSync(new URL(`../src/${p}`, import.meta.url), "utf8");

test("the three views, and nothing else, are views", () => {
  assert.deepEqual(AGENT_VIEWS, ["conversation", "terminal", "details"]);
  for (const v of AGENT_VIEWS) assert.equal(isAgentView(v), true);
  for (const v of [undefined, null, "", "chat", "Terminal", "log", 3, {}]) assert.equal(isAgentView(v), false);
});

test("a new agent opens on Chat; a harness with only its screen opens there", () => {
  assert.equal(defaultAgentView(true), "conversation");
  assert.equal(defaultAgentView(false), "terminal");
  assert.equal(resolveAgentView(undefined, undefined, true), "conversation");
  assert.equal(resolveAgentView(undefined, undefined, false), "terminal");
});

test("the address wins, then what is remembered, then the default", () => {
  assert.equal(resolveAgentView("details", "terminal", true), "details");
  assert.equal(resolveAgentView("conversation", "terminal", true), "conversation");
  assert.equal(resolveAgentView(undefined, "terminal", true), "terminal");
  assert.equal(resolveAgentView(undefined, "conversation", false), "conversation");
  // an unknown tab in the address is ignored, not an error
  assert.equal(resolveAgentView("nonsense", "terminal", true), "terminal");
  assert.equal(resolveAgentView("nonsense", undefined, true), "conversation");
  // Details is never somewhere to come back to
  assert.equal(resolveAgentView(undefined, "details", true), "conversation");
});

test("the view does not depend on the mode the agent runs in", () => {
  // resolveAgentView takes no mode at all: an agent in its own screen still opens on Chat,
  // and one in chat mode can show Terminal (which then explains there is no screen).
  assert.equal(resolveAgentView.length, 3);
});

test("remembered per agent: one agent's choice does not move another's", () => {
  let m = {};
  m = withAgentView(m, "a", "terminal");
  m = withAgentView(m, "b", "conversation");
  assert.deepEqual(m, { a: "terminal", b: "conversation" });
  m = withAgentView(m, "a", "conversation");
  assert.deepEqual(m, { b: "conversation", a: "conversation" });
  assert.deepEqual(Object.keys(m), ["b", "a"], "the newest choice is last");
});

test("Details and empty ids are not remembered, and leave the map untouched", () => {
  const m = { a: "terminal" };
  assert.equal(withAgentView(m, "a", "details"), m);
  assert.equal(withAgentView(m, "", "terminal"), m);
});

test("the map keeps the most recent agents only", () => {
  let m = {};
  for (let i = 0; i < 10; i++) m = withAgentView(m, `a${i}`, "terminal", 4);
  assert.deepEqual(Object.keys(m), ["a6", "a7", "a8", "a9"]);
  m = withAgentView(m, "a6", "conversation", 4);
  assert.deepEqual(Object.keys(m), ["a7", "a8", "a9", "a6"], "choosing again keeps an old agent");
});

test("a damaged or foreign saved value reads as nothing remembered", () => {
  for (const raw of [null, undefined, "", "{", "[]", "3", '"terminal"', "null"]) assert.deepEqual(parseAgentViews(raw), {});
  assert.deepEqual(parseAgentViews('{"a":"terminal","b":"details","c":7,"d":"conversation","":"terminal"}'), { a: "terminal", d: "conversation" });
});

test("survives a reload through localStorage, and works without it", async () => {
  const store = new Map();
  globalThis.localStorage = {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => void store.set(k, String(v)),
  };
  try {
    rememberAgentView("ag1", "terminal");
    rememberAgentView("ag2", "conversation");
    rememberAgentView("ag2", "details"); // not kept
    assert.equal(rememberedAgentView("ag1"), "terminal");
    assert.equal(rememberedAgentView("ag2"), "conversation");
    assert.equal(rememberedAgentView("never-opened"), undefined);
    assert.deepEqual(parseAgentViews(store.get("tmx.agentViews")), { ag1: "terminal", ag2: "conversation" });
    // a fresh page: a new copy of the module reads what the last one saved
    const again = await import("../src/lib/agent-view.ts?reload=1");
    assert.equal(again.rememberedAgentView("ag1"), "terminal");
    // a browser that refuses to store still remembers until the page goes
    globalThis.localStorage.setItem = () => {
      throw new Error("QuotaExceededError");
    };
    rememberAgentView("ag3", "terminal");
    assert.equal(rememberedAgentView("ag3"), "terminal");
  } finally {
    delete globalThis.localStorage;
  }
});

// The rule itself, held in the source. Under the classic UI profile opening or leaving a tab
// must never ask the daemon to change the agent (the default dashboard still calls switchMode
// from the tab's click, and keeps doing so: nothing is removed for Mustneer's line).
test("classic: the agent page's tab click only changes the view", () => {
  const page = src("pages/agent.tsx");
  const show = page.match(/function showView\([\s\S]*?\n}\n/)?.[0] ?? "";
  assert.ok(show, "showView is there");
  assert.doesNotMatch(show, /switchMode|setMode|api\./, "no daemon call from a tab click");
  assert.match(show, /rememberAgentView\(agentId, v\)/);
  assert.match(show, /navigate\(paths\.agent\(agentId, v\)/);
  // the classic branch returns before the default dashboard's mode switch
  const click = page.slice(page.indexOf("onValueChange={(v) => {"), page.indexOf("<TabsList"));
  assert.ok(click.indexOf("if (classic) return showView(agent.id, v);") >= 0, "classic takes showView");
  assert.ok(click.indexOf("if (classic) return showView(agent.id, v);") < click.indexOf("switchMode"), "and returns before switchMode");
  assert.match(page, /resolveAgentView\(tab, rememberedAgentView\(id\), chatCapable\)/);
  assert.match(page, /\{classic \|\| phone \? "Chat" : "Conversation"\}/);
  assert.match(page, /\{classic \|\| phone \? "Terminal" : harnessLabel\(agent\.harness\)\}/);
});

test("classic: no card, bar or notice; the message box is under Terminal; the mode change is a button", () => {
  const term = src("components/agent-terminal.tsx");
  for (const gone of ["Don't switch", "Back to chat", "Stay here", "Got it", "EmptyState"]) {
    assert.ok(!term.includes(gone), `the classic Terminal view has no "${gone}"`);
  }
  assert.match(term, /Open \{label\}'s screen/);
  assert.match(term, /switchMode\(agent, "terminal", true\)/, "the change is the button's, and says what the daemon answered");
  const page = src("pages/agent.tsx");
  assert.match(page, /current === "terminal" && classic && \(\s*<>\s*<AgentTerminal agent=\{agent\} \/>/);
  assert.match(page, /<MessageBox agent=\{agent\} composerRef=\{composer\} bare \/>/);
  assert.match(page, /current === "terminal" && !classic && <HarnessScreen agent=\{agent\} \/>/, "the default dashboard keeps its screen view");
  const conv = src("components/conversation/conversation.tsx");
  assert.match(conv, /\{!classic && <ScreenNotice agent=\{agent\} \/>\}/, "the notice above the message box is the default dashboard's only");
});
