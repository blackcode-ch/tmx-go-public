// The calm top under the classic UI profile (the owner #3, #9, #14, 17 Nov: "no permanent bars at
// the top; the Python dashboard's calm layout is the reference"; architect's decisions of 15:05):
// pins gone, approval toasts gone, Python's text size, a one-line agent header, no mail badge or
// usage numbers, a quiet conversation, the dictation state inline, and the Architect's
// conversation as the start page on a computer. All of it only when classic is on.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { TEXT_SIZES, textSizePercent } from "../src/lib/text-size.ts";
import { agentLine, headWord, levelWord } from "../src/lib/ui-profile.ts";
import { classicShows } from "../src/components/conversation/build.ts";

const src = (p) => readFileSync(new URL(`../src/${p}`, import.meta.url), "utf8");

test("classic text is Python's size: Normal is 100%, Large and Larger keep their proportion", () => {
  assert.equal(textSizePercent("normal", true), 100);
  assert.equal(textSizePercent("large", true), 112);
  assert.equal(textSizePercent("larger", true), 128);
  assert.equal(textSizePercent("nonsense", true), 100);
  // the default dashboard is untouched
  for (const t of TEXT_SIZES) assert.equal(textSizePercent(t.id), t.percent);
  assert.equal(textSizePercent("normal", false), 125);
});

test("the classic text size follows the daemon's profile and is re-applied when it changes", () => {
  const ts = src("lib/text-size.ts");
  assert.match(ts, /export function setClassicText\(on: boolean\)/);
  assert.match(ts, /textSizePercent\(s, classicText \?\? savedClassic\(\)\)/, "every apply uses the classic base");
  const app = src("App.tsx");
  assert.match(app, /if \(status === "ready"\) setClassicText\(classic\);/, "only once the snapshot said which profile it is");
});

test("the agent header's one line is Python's: project · machine · role", () => {
  assert.deepEqual(agentLine({ role: "backend", level: "worker", project_id: "p1" }, "billing-portal", "hostc"), ["billing-portal", "hostc", "backend"]);
  // no role: the level in words; the head is the Architect
  assert.deepEqual(agentLine({ role: "", level: "lead", project_id: "p1" }, "billing-portal", "hostc"), ["billing-portal", "hostc", "Lead"]);
  assert.deepEqual(agentLine({ level: "head" }, undefined, "hostc"), ["hostc", "Architect"]);
  // a project name without a project on the agent is not shown; a missing machine is skipped
  assert.deepEqual(agentLine({ role: "tests", level: "worker" }, "stale", ""), ["tests"]);
});

test("classic says Architect where the default dashboard says head", () => {
  assert.equal(headWord(true), "Architect");
  assert.equal(headWord(true, true), "Architect");
  assert.equal(headWord(false), "head");
  assert.equal(headWord(false, true), "Head");
  assert.equal(levelWord("head", true), "Architect");
  assert.equal(levelWord("head", false), "Head");
  assert.equal(levelWord("lead", true), "Lead");
  assert.equal(levelWord("worker", false), "Worker");
});

test("the classic conversation keeps messages and tools, drops Thinking, time breaks and Done dividers", () => {
  const e = (extra = {}) => ({ seq: 1, agent_id: "a", at: 0, kind: "assistant", text: "", ...extra });
  for (const type of ["user", "assistant", "mail", "system", "error"]) assert.equal(classicShows({ type, key: type, e: e() }), true, type);
  assert.equal(classicShows({ type: "tools", key: "t", tools: [] }), true);
  assert.equal(classicShows({ type: "thinking", key: "th", e: e() }), false);
  assert.equal(classicShows({ type: "time", key: "tm", at: 0 }), false);
  assert.equal(classicShows({ type: "result", key: "r", e: e({ meta: { subtype: "success", num_turns: 3 } }) }), false);
  assert.equal(classicShows({ type: "result", key: "r", e: e() }), false);
  // a turn that failed still says so
  assert.equal(classicShows({ type: "result", key: "r", e: e({ is_error: true }) }), true);
  assert.equal(classicShows({ type: "result", key: "r", e: e({ meta: { subtype: "error_max_turns" } }) }), true);
  const conv = src("components/conversation/conversation.tsx");
  assert.match(conv, /conversationItems\(buildItems\(entries\), classic, showAgentMessages,/);
  assert.match(src("components/conversation/build.ts"), /if \(classic && !classicShows\(it\)\) continue;/);
});

test("classic has no pins: no strip above the page, no Pin button on a message, nothing loaded", () => {
  const pins = src("components/message-pins.tsx");
  const button = pins.slice(pins.indexOf("export function PinMessageButton"), pins.indexOf("export function TimelinePin"));
  assert.match(button, /if \(useClassic\(\)\) return null;/);
  assert.ok(button.indexOf("if (useClassic()) return null;") < button.indexOf("<button"), "before the button's markup");
  const strip = pins.slice(pins.indexOf("export function MessagePinsStrip"));
  const gone = strip.indexOf("if (classic) return null;");
  assert.ok(gone > strip.lastIndexOf("useEffect("), "after every hook");
  assert.ok(gone > 0 && gone < strip.indexOf("<section"), "before the strip's markup");
  assert.match(strip, /if \(classic\) return; \/\/ classic has no pins/, "and it does not ask the daemon for pins");
});

test("classic shows no approval toasts; the count on Your turn is the signal", () => {
  const app = src("App.tsx");
  const hook = app.slice(app.indexOf("function useApprovalToasts"), app.indexOf("function Boot"));
  const off = hook.indexOf("if (classicNow()) return;");
  assert.ok(off > 0 && off < hook.indexOf("toast("), "returns before any toast is made");
});

test("classic on a computer opens on the Architect's conversation; a phone keeps Home", () => {
  const app = src("App.tsx");
  const routes = app.slice(app.indexOf("function Routes"), app.indexOf("function MobileBar"));
  assert.match(routes, /const desktop = useMedia\("\(min-width: 768px\)"\);/);
  assert.match(routes, /if \(classic && desktop && headId\) return <AgentPage key=\{headId\} id=\{headId\} \/>;\s*return <OverviewPage \/>;/);
});

test("the classic agent page: one header line, no badges, no mail badge, no usage numbers, no hand-off", () => {
  const page = src("pages/agent.tsx");
  assert.match(page, /\{!classic && !phone && <LevelBadge level=\{agent\.level\} \/>\}/);
  assert.match(page, /\{!phone && !classic && <ModelBadge agent=\{agent\}/);
  assert.match(page, /\{!phone && !classic && isCustomProfile\(agent\)/);
  assert.match(page, /agentLine\(agent, project\?\.name, machine\)/);
  assert.match(page, /\{!classic && <MailReceipts agentId=\{agent\.id\} \/>\}/);
  assert.match(page, /\{!classic && <AgentUsageInline agent=\{agent\} \/>\}/);
  assert.match(page, /agent\.level === "head" && !classic && !compact && \(/, "no Hand to Companion button in the header");
  assert.match(page, /\(agent\.level !== "head" \|\| compact\) && !classic && \(\s*<DropdownMenuItem onSelect=\{\(\) => openHandOff/, "nor in the menu");
  // honest wording in both profiles: it deletes the agent with its conversation, so Python's
  // "Remove from list" waits for a real shelve on the fleet line (tmx-lead, 17 Nov)
  assert.match(page, /<Trash2 \/> Delete agent…/);
  assert.doesNotMatch(page, /Remove from list…"/);
  assert.match(page, /disabled=\{!busy\} onSelect=\{\(\) => void run\(agent, "interrupt"\)\}/, "Interrupt stays (architect, 15:05)");
});

test("classic keeps the dictation state in the message box; the top bar is only for a box on another page", () => {
  const bar = src("components/dictation-bar.tsx");
  const pending = bar.indexOf('if (phase === "working" && pending) return null;');
  const aside = bar.indexOf("if (classic && inline) return null;");
  assert.ok(pending > 0 && aside > pending && aside < bar.indexOf("data-dictation-bar"), "steps aside after the hooks, before the markup");
  assert.match(bar, /useInlineDictation\(\(s\) => \(target \? \(s\.boxes\[target\.id\] \?\? 0\) > 0 : false\)\)/, "only while that very box shows the state");
  const inline = src("components/dictation-inline.tsx");
  assert.match(inline, /if \(!mine \|\| phase === "idle"\) return <>/, "dictation and recovery status stay in the message box");
  assert.match(inline, /c\.target\.id === boxId && c\.text === undefined/, "only this composer's pending clips show inline");
  assert.match(inline, /if \(phase === "working" && pending\) return null;/, "no indicator for a transcription waiting to be retried");
  assert.match(inline, /stopDictating\(\)/);
  assert.match(inline, /cancelDictating\(\)/);
  const conv = src("components/conversation/conversation.tsx");
  assert.doesNotMatch(conv, /<DictationInline/, "the conversation must not duplicate the composer status");
  const composer = src("components/conversation/composer.tsx");
  assert.match(composer, /<DictationInline boxId=\{"agent:" \+ key\} \/>/, "classic and default both keep status in the composer");
  assert.ok(composer.indexOf('<DictationInline') < composer.indexOf('data-composer-actions'), "status stays outside the action row");
  // the composer registers under the same id
  assert.match(src("components/conversation/composer.tsx"), /draft=\{\{ id: "agent:" \+ key,/);
});

test("classic uses Python's page padding and chat width; the default dashboard keeps its own", () => {
  const page = src("components/page.tsx");
  assert.match(page, /const pad = classic \? "px-\[1\.125rem\] py-2\.5" : "px-5 py-6 md:px-8 md:py-8";/);
  assert.match(page, /cn\(pad, cn\("mx-auto min-w-0 w-full", width, className\)\)/, "a page's own classes still win over the padding");
  const conv = src("components/conversation/conversation.tsx");
  assert.match(conv, /const CLASSIC_COLUMN = "max-w-\[65rem\]";/, "1040 px at the classic text size");
  assert.match(conv, /classic \? CLASSIC_COLUMN : "max-w-\[48\.75rem\]"/);
  assert.match(conv, /classic \? CLASSIC_BOX : "max-w-\[47\.25rem\]"/);
});

test("classic shows no key hint under the message box", () => {
  assert.match(src("components/conversation/composer.tsx"), /\{classic\s*\? "" \/\/ classic: no key hint/);
});
