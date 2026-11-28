// Chat and Terminal side by side (src/lib/agent-view.ts; parity B1: Python's "Split workspace" /
// "Focus view"), under the classic UI profile on a wide screen.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { DEFAULT_PANES, parsePanes, savedPanes, savePanes, showPane, splitApplies, SPLIT_MIN_WIDTH, togglePane, visiblePaneView, resolveAgentView } from "../src/lib/agent-view.ts";

const src = (p) => readFileSync(new URL(`../src/${p}`, import.meta.url), "utf8");

test("on a wide screen the panes start side by side, both showing, as in Python", () => {
  assert.deepEqual(DEFAULT_PANES, { split: true, chat: true, terminal: true });
  assert.equal(SPLIT_MIN_WIDTH, 1280);
});

test("each button shows or hides its pane; the last one showing stays", () => {
  const both = DEFAULT_PANES;
  const chatOnly = togglePane(both, "terminal");
  assert.deepEqual(chatOnly, { split: true, chat: true, terminal: false });
  assert.equal(togglePane(chatOnly, "chat"), chatOnly, "never an empty workspace");
  assert.deepEqual(togglePane(chatOnly, "terminal"), both);
  const termOnly = togglePane(both, "chat");
  assert.deepEqual(termOnly, { split: true, chat: false, terminal: true });
  assert.equal(togglePane(termOnly, "terminal"), termOnly);
  assert.notEqual(togglePane(both, "chat"), both, "a new object, so the page re-renders");
});

test("coming back from Details by a pane's button makes sure that pane shows", () => {
  const termOnly = { split: true, chat: false, terminal: true };
  assert.deepEqual(showPane(termOnly, "chat"), { split: true, chat: true, terminal: true });
  assert.equal(showPane(termOnly, "terminal"), termOnly);
});

test("side by side only under classic, on a wide screen, by choice, for an agent with a chat and a screen", () => {
  const yes = { classic: true, wide: true, layout: DEFAULT_PANES, chatCapable: true, harness: "claude" };
  assert.equal(splitApplies(yes), true);
  assert.equal(splitApplies({ ...yes, harness: "codex" }), true);
  assert.equal(splitApplies({ ...yes, classic: false }), false, "the default dashboard keeps its tabs");
  assert.equal(splitApplies({ ...yes, wide: false }), false, "a narrow window shows one view at a time");
  assert.equal(splitApplies({ ...yes, layout: { ...DEFAULT_PANES, split: false } }), false, "Focus view");
  assert.equal(splitApplies({ ...yes, chatCapable: false, harness: "opencode" }), false);
  assert.equal(splitApplies({ ...yes, harness: "shell", chatCapable: false }), false);
});

test("a damaged saved layout is the default; one with nothing showing gets its chat back", () => {
  for (const raw of [null, undefined, "", "{", "[]", "7", "null"]) assert.deepEqual(parsePanes(raw), DEFAULT_PANES);
  assert.deepEqual(parsePanes('{"split":false}'), { split: false, chat: true, terminal: true });
  assert.deepEqual(parsePanes('{"split":true,"chat":false,"terminal":true}'), { split: true, chat: false, terminal: true });
  assert.deepEqual(parsePanes('{"chat":false,"terminal":false}'), { split: true, chat: true, terminal: false });
});

test("the layout is one choice for all agents, remembered on the device", () => {
  const store = new Map();
  globalThis.localStorage = { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => void store.set(k, String(v)) };
  try {
    assert.deepEqual(savedPanes(), DEFAULT_PANES);
    savePanes({ split: false, chat: true, terminal: true });
    assert.deepEqual(savedPanes(), { split: false, chat: true, terminal: true });
    assert.equal(store.size, 1, "one key, not one per agent");
  } finally {
    delete globalThis.localStorage;
  }
  assert.deepEqual(savedPanes(), DEFAULT_PANES, "no storage: the default, and no throw");
  savePanes(DEFAULT_PANES);
});

test("the agent page lays the panes out side by side with the message box underneath", () => {
  const page = src("pages/agent.tsx");
  assert.match(page, /const wide = useMedia\(`\(min-width: \$\{SPLIT_MIN_WIDTH\}px\)`\);/);
  assert.match(page, /const split = splitApplies\(\{ classic, wide, layout: panes, chatCapable, harness: agent\?\.harness \}\);/);
  assert.match(page, /<Conversation agent=\{agent\} composerRef=\{composer\} hideBox \/>/, "the chat pane without its own message box");
  assert.match(page, /data-agent-split/);
  const splitBlock = page.slice(page.indexOf("data-agent-split"));
  assert.ok(splitBlock.indexOf("<MessageBox agent={agent} composerRef={composer} />") > 0, "one message box, full width, under both panes");
  assert.match(page, /\{panes\.split \? "Focus view" : "Split workspace"\}/, "Python's words");
  const conv = src("components/conversation/conversation.tsx");
  assert.match(conv, /\{!hideBox && <MessageBox agent=\{agent\} composerRef=\{composerRef\} compact \/>\}/);
});

test('narrowing keeps Chat after split hid Terminal or the owner chose Chat',()=>{
 const layout={split:true,chat:true,terminal:false};
 // A stale remembered Terminal view cannot replace the only pane the owner saw.
 const selected=visiblePaneView(layout,resolveAgentView(undefined,'terminal',true));
 assert.equal(selected,'conversation');
 for(const wide of [true,false,true,false]){
  const split=splitApplies({classic:true,wide,layout,chatCapable:true,harness:'codex'});
  assert.equal(split,wide);
  assert.equal(resolveAgentView(selected,'terminal',true),'conversation');
 }
 assert.equal(visiblePaneView({...layout,chat:false,terminal:true},'conversation'),'terminal');
 const page=src('pages/agent.tsx');
 assert.match(page,/visiblePaneView\(panes, current\)/);
 assert.match(page,/if \(visible !== current\) showView\(id, visible\)/);
 assert.match(page,/data-pane="chat" onPointerDownCapture=[\s\S]*?showView\(agent.id, "conversation"\)/);
 assert.match(page,/shrink-0 border-t bg-background[^\n]*onFocusCapture=[^\n]*showView\(agent.id, "conversation"\)/,'typing in the split composer selects Chat for a narrow window');
 assert.match(page,/autoFocus=\{!split\}/,'split terminals cannot steal Chat selection on mount');
});
