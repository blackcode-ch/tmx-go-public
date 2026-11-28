// The phone in the classic UI profile (lib/phone.ts): the Python dashboard's mobile app, one to
// one. A bottom bar with Home, Projects, Agents and Your turn instead of a top bar and a menu;
// nothing fixed above a page; Python's own sizes. With the profile off a phone shows the default
// dashboard exactly as before.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  PHONE_HOME_ROWS,
  PHONE_PX,
  PHONE_TABS,
  agentHeaderLine,
  agentStateWords,
  askingAgents,
  homeAgents,
  isTabRoot,
  phoneAgentRows,
  phoneBackPath,
  phoneProjectTiles,
  phoneTabOf,
  projectTileLine,
  stateTone,
  tabCount,
} from "../src/lib/phone.ts";

const read = (f) => readFileSync(new URL(f, import.meta.url), "utf8");
const agent = (o) => ({ id: o.name, level: "worker", profile: "worker", role: "", harness: "claude", state: "idle", last_activity_at: 0, ...o });
const projects = {
  p1: { id: "p1", name: "Clinic site", status: "active", updated_at: 10, brief: "" },
  p2: { id: "p2", name: "Archive", status: "archived", updated_at: 99, brief: "" },
  p3: { id: "p3", name: "Billing", status: "active", updated_at: 50, brief: "" },
};

test("bottom bar: Home, Projects, Agents, Your turn, in the Python order", () => {
  assert.deepEqual(PHONE_TABS.map((t) => t.label), ["Home", "Projects", "Agents", "Your turn"]);
  assert.deepEqual(PHONE_TABS.map((t) => t.path), ["/", "/projects", "/agents", "/inbox"]);
});

test("bottom bar: a page lights its tab; the other pages light none", () => {
  assert.equal(phoneTabOf("overview"), "home");
  assert.equal(phoneTabOf("projects"), "projects");
  assert.equal(phoneTabOf("project"), "projects");
  assert.equal(phoneTabOf("agents"), "agents");
  assert.equal(phoneTabOf("agent"), "agents");
  assert.equal(phoneTabOf("inbox"), "turn");
  for (const name of ["settings", "people", "clients", "chats", "chat", "matrix", "recordings", "notfound"]) assert.equal(phoneTabOf(name), null, name);
});

test("bottom bar: the count on Your turn", () => {
  assert.equal(tabCount(0), "");
  assert.equal(tabCount(3), "3");
  assert.equal(tabCount(9), "9");
  assert.equal(tabCount(10), "9+");
});

test("Python's sizes", () => {
  assert.deepEqual(PHONE_PX, { bottomBar: 76, titleRow: 66, agentRow: 66, plainRow: 48, choice: 48, target: 44, smallestText: 12, column: 480 });
});

test("bottom bar: 76 px with 12 px labels, the last row of a phone in the classic profile only", () => {
  const app = read("../src/App.tsx");
  assert.ok(/<Routes \/>\s*<PhoneBottomBar \/>\s*<\/main>/.test(app), "the bar is the last row of main");
  const bar = read("../src/components/phone/bottom-bar.tsx");
  assert.ok(bar.includes("const phone = usePhoneClassic();") && bar.includes("if (!phone) return null;"), "classic phone only");
  // 7 px above + 59 px tabs + 9 px below + the 1 px border = Python's 76 px; each tab >= 48 px
  assert.ok(bar.includes("pt-[7px]") && bar.includes("h-[59px]") && bar.includes("pb-[max(9px,env(safe-area-inset-bottom))]") && bar.includes("border-t"));
  assert.equal(7 + 59 + 9 + 1, PHONE_PX.bottomBar);
  assert.ok(bar.includes("text-[12px]"), "12 px labels");
  assert.ok(!/text-2xs|text-xs|text-\[1[01]px\]/.test(bar), "nothing under 12 px");
  // the routes the tabs open exist, also for the classic route guard
  const router = read("../src/lib/router.ts");
  assert.ok(router.includes('if (parts[0] === "agents" && parts.length === 1) return { name: "agents" };'));
  assert.ok(router.includes('if (parts[0] === "projects" && parts.length === 1) return { name: "projects" };'));
  assert.ok(app.includes('case "agents":\n      return <AgentsPage />;'));
  assert.ok(app.includes('case "projects":\n      return <ProjectsPage />;'));
});

test("classic off: a phone keeps the top bar, the menu and Home exactly as they were", () => {
  const app = read("../src/App.tsx");
  const bar = app.slice(app.indexOf("function MobileBar()"), app.indexOf("function MobileNav()"));
  assert.ok(bar.includes('className="flex h-12 shrink-0 items-center gap-1 border-b bg-sidebar px-2 md:hidden"'));
  assert.ok(bar.includes('aria-label="Open navigation"') && bar.includes('aria-label="Search"') && bar.includes('aria-label="Needs you"'));
  assert.ok(app.includes("{!phone && <MobileBar />}"), "the top bar goes only on a phone in the classic profile");
  assert.ok(app.includes("if (phone) return <PhoneHome />;"), "Python's Home only there");
  const hook = read("../src/hooks/use-phone-classic.ts");
  assert.ok(hook.includes("return classic && phone;"), "both the profile and a phone-sized screen");
  assert.ok(hook.includes('export const PHONE_QUERY = "(max-width: 767px)";'));
  // the title row of every page: the old one unless it is a phone in the classic profile
  const header = read("../src/components/page.tsx");
  assert.ok(header.includes('"drag-region flex min-h-[3.25rem] shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b px-5 py-2 md:flex-nowrap md:px-6 md:py-0"'));
});

test("classic on a phone: nothing fixed above a tab's page; other pages get one 66 px row with a back arrow", () => {
  for (const name of ["overview", "projects", "agents", "inbox"]) assert.equal(isTabRoot(name), true, name);
  for (const name of ["agent", "project", "settings", "people", "matrix", "recordings", "chat"]) assert.equal(isTabRoot(name), false, name);
  const header = read("../src/components/page.tsx");
  assert.ok(header.includes('if (isTabRoot(route.name)) return <header className="hidden">{children}</header>;'), "hidden, its children still mounted");
  assert.ok(header.includes("min-h-[66px]") && header.includes('aria-label="Back"') && header.includes("size-11"));
  assert.ok(PHONE_PX.titleRow <= 125, "what stands above a page is well under the 125 px that were asked for");
});

test("the back arrow: an agent back to Agents, a project to Projects, the rest to Home", () => {
  assert.equal(phoneBackPath({ name: "agent", id: "a1" }), "/agents");
  assert.equal(phoneBackPath({ name: "project", id: "p1" }), "/projects");
  assert.equal(phoneBackPath({ name: "chat", id: "c1" }), "/chats");
  assert.equal(phoneBackPath({ name: "profile" }), "/settings");
  assert.equal(phoneBackPath({ name: "recordings", id: "r1" }), "/recordings");
  assert.equal(phoneBackPath({ name: "recordings" }), "/");
  assert.equal(phoneBackPath({ name: "whatsapp", jid: "x@s" }), "/whatsapp");
  assert.equal(phoneBackPath({ name: "whatsapp" }), "/");
  for (const name of ["settings", "people", "clients", "promises", "matrix", "usage", "companion", "chats", "notfound"]) assert.equal(phoneBackPath({ name }), "/", name);
});

test("classic: no notifications banner; Home's Notifications row opens the push setting", () => {
  const banner = read("../src/components/phone-notifications.tsx");
  assert.ok(banner.includes('if (classic || prompt === "hidden") return null;'));
  assert.equal(PHONE_HOME_ROWS[0].label, "Notifications");
  assert.equal(PHONE_HOME_ROWS[0].path, "/settings?section=phone-notifications");
  assert.ok(banner.includes('<Section title="Phone notifications">'), 'the section whose id is "phone-notifications"');
});

test("Home's plain rows: Python's, the pages that are kept, and Settings; 48 px each", () => {
  const labels = PHONE_HOME_ROWS.map((r) => r.label);
  for (const python of ["Notifications", "Companion", "WhatsApp", "People"]) assert.ok(labels.includes(python), python);
  assert.ok(!labels.includes("Promises"), "Promises is not a place any more: the Matrix replaced it (the owner, 17 Nov 18:55)");
  for (const kept of ["Recordings", "WhatsApp", "Matrix", "Usage and accounts"]) assert.ok(labels.includes(kept), kept);
  assert.ok(labels.includes("Settings"));
  assert.ok(!labels.includes("Features"), "hidden in the classic profile");
  assert.equal(new Set(PHONE_HOME_ROWS.map((r) => r.path)).size, PHONE_HOME_ROWS.length, "no row twice");
  for (const r of PHONE_HOME_ROWS) assert.ok(r.path.startsWith("/") && r.hint.length > 0, r.label);
  // every entry of the classic sidebar's main list has its row (a phone has no sidebar)
  const sidebar = read("../src/components/sidebar.tsx");
  for (const [label, path] of [["Matrix", "/matrix"], ["Recordings", "/recordings"], ["People", "/people"], ["Usage and accounts", "/usage"]]) {
    assert.ok(sidebar.includes(`"${label}"`), `the sidebar says ${label}`);
    assert.equal(PHONE_HOME_ROWS.find((r) => r.label === label)?.path, path);
  }
  assert.ok(!labels.includes("Clients"), "classic People is one entry, with the tabs Team | Clients & partners");
  const home = read("../src/pages/phone-home.tsx");
  assert.ok(/className="flex min-h-12 items-center [^"]*"\s*data-phone-home-row/.test(home), "min-h-12 = 48 px at the classic text size");
  // Python's order: headline, Your turn card, disk per machine, conversations, projects, rows
  const order = ["greeting(new Date().getHours())", "<YourTurnCard />", "<DiskRows />", 'title="Pick up a conversation"', 'title="Moving forward"', "PHONE_HOME_ROWS.map"];
  const at = order.map((s) => home.lastIndexOf(s));
  assert.ok(at.every((i) => i > 0), "every part is there");
  assert.deepEqual([...at].sort((a, b) => a - b), at, "in Python's order");
  for (const gone of ["Composer", "ActivityFeed", "CompanionStrip", "MachineStrip />"]) assert.ok(!home.includes(gone), gone + " is not on the phone's Home");
});

test("Home: the Architect first, then what the list puts on top, three in all", () => {
  const rows = phoneAgentRows(
    [agent({ name: "w1", state: "waiting" }), agent({ name: "w2", state: "working" }), agent({ name: "w3", state: "working", last_activity_at: 5 }), agent({ name: "boss", level: "head", profile: "head" })],
    projects,
  );
  assert.deepEqual(homeAgents(rows, "boss").map((r) => r.agent.name), ["boss", "w1", "w3"]);
  assert.deepEqual(homeAgents(rows, null).map((r) => r.agent.name), ["w1", "w3", "w2"]);
  assert.deepEqual(homeAgents(rows, "not-there").map((r) => r.agent.name), ["w1", "w3", "w2"]);
  assert.deepEqual(homeAgents([], "boss"), []);
});

test("state in Python's words, with what a phone has no other room for", () => {
  assert.equal(agentStateWords({ state: "working" }), "working");
  assert.equal(agentStateWords({ state: "waiting" }), "needs you");
  assert.equal(agentStateWords({ state: "waiting" }, true), "asks you");
  assert.equal(agentStateWords({ state: "error" }), "failed");
  assert.equal(agentStateWords({ state: "queued", queue_pos: 2 }), "queued · 2nd");
  assert.equal(agentStateWords({ state: "queued", queue_pos: 11 }), "queued · 11th");
  assert.equal(agentStateWords({ state: "queued" }), "queued");
  assert.equal(agentStateWords({ state: "paused", paused_reason: "write_budget" }), "paused · write budget");
  assert.equal(agentStateWords({ state: "paused", paused_reason: "something-new" }), "paused");
  assert.equal(agentStateWords({ state: "idle", shelved_at: 5 }), "shelved");
  assert.equal(stateTone({ state: "waiting" }), "you");
  assert.equal(stateTone({ state: "paused" }), "you");
  assert.equal(stateTone({ state: "working" }), "moving");
  assert.equal(stateTone({ state: "error" }), "fault");
  assert.equal(stateTone({ state: "idle" }), "quiet");
  assert.equal(stateTone({ state: "working", shelved_at: 5 }), "quiet");
  assert.deepEqual(
    [...askingAgents([
      { agent_id: "a", state: "pending", kind: "question" },
      { agent_id: "b", state: "pending", kind: "permission" },
      { agent_id: "c", state: "pending", kind: "question", deferred: true },
      { agent_id: "d", state: "answered", kind: "question" },
    ])],
    ["a"],
  );
});

test("Agents: one flat list, what needs you first, then what works, then by last activity", () => {
  const list = [
    agent({ name: "idle-old", last_activity_at: 1 }),
    agent({ name: "worker-b", state: "working", project_id: "p1", role: "frontend" }),
    agent({ name: "idle-new", last_activity_at: 9 }),
    agent({ name: "asks", state: "waiting", project_id: "p3", level: "lead", profile: "lead" }),
    agent({ name: "boss", state: "working", level: "head", profile: "head" }),
    agent({ name: "broken", state: "error" }),
    agent({ name: "done", state: "stopped", last_activity_at: 50 }),
  ];
  const rows = phoneAgentRows(list, projects, "", { asks: new Set(["asks"]) });
  assert.deepEqual(rows.map((r) => r.agent.name), ["asks", "broken", "boss", "worker-b", "idle-new", "idle-old", "done"]);
  assert.equal(rows[0].sub, "Billing · lead");
  assert.equal(rows[0].state, "asks you");
  assert.equal(rows[1].state, "failed");
  assert.equal(rows[3].sub, "Clinic site · frontend");
  assert.equal(rows[2].sub, "architect");
  assert.equal(rows[4].sub, "worker");
});

test("Agents: the classic rule and line, the computer's (every agent except the stopped ones)", () => {
  const page = read("../src/pages/phone-lists.tsx");
  assert.ok(page.includes("all.filter((a) => classicAgentVisible(a, showStopped))"));
  assert.ok(page.includes("stoppedLine(stoppedAgentCount(all), showStopped)") && page.includes("{stopped.text} · {stopped.action}"));
  assert.ok(page.includes("saveShowStopped(!showStopped);") && page.includes("ui.set({ showStoppedAgents: !showStopped });"), "the same remembered choice as the sidebar");
  assert.ok(page.includes('placeholder="Search agents, projects, machines"'));
  assert.ok(!page.includes("AgentListFilter") && !page.includes("useVisibleAgents"), "not the two-hour rule of the default dashboard");
  assert.ok(page.includes("ui.openNewAgent(true)"), 'Python\'s "+ Agent"');
});

test("Agents: the search looks at the name, the project, the role, the machine and the state in words", () => {
  const list = [
    agent({ name: "alpha", state: "working", project_id: "p1", role: "frontend" }),
    agent({ name: "beta", state: "waiting", project_id: "p3" }),
    agent({ name: "gamma", project_id: "p3", profile: "reviewer" }),
  ];
  const names = (q, opts) => phoneAgentRows(list, projects, q, opts).map((r) => r.agent.name);
  assert.deepEqual(names("ALPHA"), ["alpha"]);
  assert.deepEqual(names("billing"), ["beta", "gamma"]);
  assert.deepEqual(names("frontend"), ["alpha"]);
  assert.deepEqual(names("reviewer"), ["gamma"]);
  assert.deepEqual(names("needs you"), ["beta"]);
  assert.deepEqual(names("studio", { machine: "studio" }), ["beta", "alpha", "gamma"], "a machine's name shows everything on it");
  assert.deepEqual(names("  "), ["beta", "alpha", "gamma"]);
  assert.deepEqual(names("nothing-like-this"), []);
});

test("Projects: a tile per project, active first, with working and agent counts", () => {
  const list = [
    agent({ name: "a", state: "working", project_id: "p1" }),
    agent({ name: "b", state: "starting", project_id: "p1" }),
    agent({ name: "c", state: "waiting", project_id: "p1" }),
    agent({ name: "d", state: "working", project_id: "p1", shelved_at: 2 }),
    agent({ name: "e", state: "idle", project_id: "p3" }),
    agent({ name: "f", state: "working" }),
  ];
  const tiles = phoneProjectTiles(projects, list);
  assert.deepEqual(tiles.map((t) => t.project.name), ["Billing", "Clinic site", "Archive"]);
  const clinic = tiles[1];
  assert.deepEqual([clinic.working, clinic.agents, clinic.needsYou], [2, 3, 1]);
  assert.equal(projectTileLine(clinic), "2 working · 3 agents · 1 needs you");
  assert.equal(projectTileLine(tiles[0]), "0 working · 1 agent");
  assert.equal(projectTileLine({ working: 0, agents: 0, needsYou: 2 }), "0 working · 0 agents · 2 need you");
});

test("rows: agents 66 px, the search field and every row a thumb target", () => {
  const lists = read("../src/pages/phone-lists.tsx");
  assert.ok(lists.includes('className="flex min-h-[66px] items-center gap-[11px] py-[13px]'), "66 px agent rows");
  assert.ok(lists.includes('type="search"') && lists.includes("min-h-11") && lists.includes("text-[16px]"), "44 px field, 16 px text (no zoom on iOS)");
  const home = read("../src/pages/phone-home.tsx");
  assert.equal((home.match(/min-h-\[66px\]/g) ?? []).length, 2, "Home's agent and project rows");
  assert.ok(!/text-2xs|text-\[1[01]px\]|text-\[0\.[0-6]/.test(home), "nothing under 12 px on Home");
});

test("the column is capped at 480 px and the small sizes are raised to 12 px, on that phone only", () => {
  const css = read("../src/index.css");
  const block = css.slice(css.indexOf("/* The classic profile on a phone (main[data-phone-classic]"), css.indexOf("/* Your turn on that phone"));
  assert.ok(block.includes("@media (max-width: 767px) {"));
  assert.ok(/main\[data-phone-classic\] \{\s*width: 100%;\s*max-width: 480px;\s*margin-inline: auto;/.test(block));
  for (const cls of [".text-2xs", ".text-xs", '[class*="text-[0.625rem]"]', '[class*="text-[0.6875rem]"]', '[class*="text-[0.7188rem]"]']) {
    assert.ok(block.includes(`main[data-phone-classic] :where(${cls}) { font-size: max(12px, `), cls);
  }
  const app = read("../src/App.tsx");
  assert.ok(app.includes('data-phone-classic={phone ? "" : undefined}'));
});

test("agent header on a phone: the state in words, then Python's project · machine · role", () => {
  const a = { state: "working", role: "frontend", level: "worker", project_id: "p1" };
  assert.deepEqual(agentHeaderLine(a, false, "Clinic site", "studio"), { state: "working", rest: "Clinic site · studio · frontend" });
  assert.deepEqual(agentHeaderLine({ ...a, state: "waiting" }, true, "Clinic site", undefined), { state: "asks you", rest: "Clinic site · frontend" });
  assert.deepEqual(agentHeaderLine({ state: "paused", paused_reason: "disk", role: "", level: "lead", project_id: "p1" }, false, "Billing", "studio"), { state: "paused · disk saturated", rest: "Billing · studio · Lead" });
  assert.deepEqual(agentHeaderLine({ state: "idle", role: "", level: "head" }, false, undefined, "studio"), { state: "idle", rest: "studio · Architect" });
});

test("agent header: phone widths use one line for the name and state, with metadata in Details", () => {
  const page = read("../src/pages/agent.tsx");
  const from = page.indexOf("<PageHeader actions={<AgentActions agent={agent} compact={phone} />}");
  const header = page.slice(from, page.indexOf("</PageHeader>", from));
  assert.ok(header.includes("{phone ? ("));
  assert.ok(/<p className="[^"]*" data-phone-agent-line>/.test(header));
  assert.ok(header.includes("{phoneLine.state}"));
  assert.ok(page.includes('<Detail label="Machine">') && page.includes('<Detail label="Role">'));
  assert.ok(header.includes('<h1 className="min-w-[4rem] truncate text-[0.875rem] font-semibold tracking-tight">{agentLabel(agent, profiles)}</h1>'), "the old name, unchanged");
  assert.ok(page.includes("const phone = useMedia(PHONE_QUERY);"));
  // the computer's classic line stays hidden below md: nothing is said twice
  assert.ok(header.includes("md:inline-flex\" data-agent-line"));
});
