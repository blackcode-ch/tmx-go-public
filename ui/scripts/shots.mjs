// Screenshots of the mock-data UI for visual review: node scripts/shots.mjs [filter]
// Serves ui/dist (only) on 127.0.0.1, drives Playwright's bundled Chromium headless, writes
// PNGs to ui/screenshots/ (git-ignored). Run `pnpm build` first. On a shared machine wrap it:
//   tmx browser-slot -- node scripts/shots.mjs
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const dist = path.join(root, "dist");
const out = path.join(root, "screenshots");
fs.mkdirSync(out, { recursive: true });
const filter = process.argv[2] ?? "";

const TYPES = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".woff2": "font/woff2", ".svg": "image/svg+xml" };
const server = http.createServer((req, res) => {
  const u = new URL(req.url, "http://x");
  let p = path.normalize(path.join(dist, decodeURIComponent(u.pathname)));
  if (!p.startsWith(dist)) return res.writeHead(403).end();
  if (fs.existsSync(p) && fs.statSync(p).isDirectory()) p = path.join(p, "index.html");
  if (!fs.existsSync(p)) return res.writeHead(404).end();
  res.writeHead(200, { "Content-Type": TYPES[path.extname(p)] ?? "application/octet-stream" });
  fs.createReadStream(p).pipe(res);
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const base = `http://127.0.0.1:${server.address().port}/`;

const SIZES = { wide: { width: 1440, height: 900 }, small: { width: 1100, height: 720 }, phone: { width: 390, height: 844 } };

// name, url (query + hash), sizes, setup(page)
// Every page, for the "readable" check (the owner, 3 Nov: "everything is too tiny"): run with
// SHOT_TEXT=normal,larger and filter "readable" (e2e.yml's shots job does, by hand).
const READABLE = [
  ["overview", "#/"],
  ["matrix", "#/matrix"],
  ["inbox", "#/inbox"],
  ["chats", "#/chats"],
  ["chat", "#/chats/g8fk2m4q7xz"],
  ["companion", "#/companion"],
  ["whatsapp", "#/whatsapp"],
  ["recordings", "#/recordings"],
  ["usage", "#/usage"],
  ["settings", "#/settings"],
  ["agent", "#/agents/hd3k9q2m7x"],
  ["project", "#/projects/pbl7n2c4ka"],
].map(([name, hash]) => [`readable-${name}`, `?mock=still${hash}`, ["wide", "phone"], async (p) => p.waitForTimeout(700)]);

/** Elements that are wider than their box without scrolling (clipped or spilling text): the
 * page itself, main and main's direct children. "" when none. */
async function overflowOf(page) {
  return page.evaluate(() => {
    const bad = [];
    const els = [document.documentElement, document.body, ...document.querySelectorAll("main, main > *")];
    for (const el of els) {
      const ox = getComputedStyle(el).overflowX;
      if (ox === "auto" || ox === "scroll") continue; // a scroll area is meant to be wider
      if (el.scrollWidth > el.clientWidth + 1) {
        const tag = el.tagName.toLowerCase() + (el.id ? "#" + el.id : "") + (typeof el.className === "string" && el.className ? "." + el.className.trim().split(/\s+/).slice(0, 3).join(".") : "");
        bad.push(`${tag} (${el.scrollWidth} > ${el.clientWidth})`);
      }
    }
    return bad.join("; ");
  });
}

const SHOTS = [
  ...READABLE,
  ["overview", "?mock=still#/", ["wide", "small", "phone"]],
  ["overview-empty", "?mock=empty#/", ["wide", "phone"]],
  ["overview-live", "?mock=1#/", ["wide"], async (p) => p.waitForTimeout(15000)],
  ["agent-head", "?mock=still#/agents/hd3k9q2m7x", ["wide", "small", "phone"]],
  [
    "agent-worker",
    "?mock=still#/agents/wk2m4n6p8q",
    ["wide", "small", "phone"],
    async (p) => {
      const sc = p.getByTestId("conversation-scroll");
      // expand the failed test run and the edit
      const rows = p.locator("button[aria-expanded]");
      const n = await rows.count();
      for (let i = 0; i < n; i++) {
        const t = (await rows.nth(i).innerText()).trim();
        if (/^Edit/.test(t)) await rows.nth(i).click();
      }
      for (let i = 0; i < n; i++) {
        const t = (await rows.nth(i).innerText()).trim();
        if (/Failed/.test(t)) {
          await rows.nth(i).click();
          break;
        }
      }
      await p.waitForTimeout(500);
      await sc.evaluate((el) => (el.scrollTop = el.scrollHeight * 0.42));
      await p.waitForTimeout(300);
    },
  ],
  ["agent-worker-live", "?mock=1#/agents/wk2m4n6p8q", ["wide", "phone"], async (p) => p.waitForTimeout(15000)],
  ["agent-approval-edit", "?mock=still#/agents/wk3n5p7r9s", ["wide"]],
  ["agent-terminal", "?mock=still#/agents/sh5r7t9v3w", ["wide"], async (p) => p.waitForTimeout(800)],
  ["agent-details", "?mock=still#/agents/wk7t9v3x5z/details", ["wide"]],
  // Usage (#/usage): AI accounts and their quota; Connect runs the harness's sign-in in a dialog.
  ["usage", "?mock=usage#/usage", ["wide", "small", "phone"], async (p) => p.waitForTimeout(400)],
  ["usage-empty", "?mock=usage-empty#/usage", ["wide", "phone"], async (p) => p.waitForTimeout(400)],
  ["usage-down", "?mock=usage-down#/usage", ["wide"], async (p) => p.waitForTimeout(400)],
  [
    "usage-connect",
    "?mock=usage#/usage",
    ["wide", "phone"],
    async (p) => {
      await p.getByRole("button", { name: "Connect Antigravity" }).click();
      await p.waitForSelector("[data-connect-dialog] .xterm-rows");
      await p.waitForTimeout(1200);
    },
  ],
  [
    "usage-connected",
    "?mock=usage#/usage",
    ["wide"],
    async (p) => {
      await p.getByRole("button", { name: "Connect Antigravity" }).click();
      await p.waitForSelector("text=Antigravity connected", { timeout: 9000 });
      await p.waitForTimeout(600);
    },
  ],
  [
    "usage-connect-failed",
    "?mock=usage-fail#/usage",
    ["wide", "phone"],
    async (p) => {
      await p.getByRole("button", { name: "Connect Antigravity" }).click();
      await p.waitForSelector("[data-connect-dialog] >> text=Try again", { timeout: 9000 });
      await p.waitForTimeout(400);
    },
  ],
  [
    "usage-signout",
    "?mock=usage#/usage",
    ["wide"],
    async (p) => {
      await p.locator("[data-account=claude]").getByRole("button", { name: "Sign out" }).click();
      await p.waitForTimeout(400);
    },
  ],
  // The Matrix (the Board folded in on 3 Nov). "matrix-thread" wide is the README's hero (docs/images/matrix.png).
  ["matrix", "?mock=still#/matrix", ["wide", "small", "phone"], async (p) => p.waitForTimeout(600)],
  [
    "matrix-thread",
    "?mock=still#/matrix",
    ["wide", "phone"],
    async (p) => {
      const fit = p.getByRole("button", { name: "Fit the board" }); // the canvas; a phone opens the list
      if (await fit.isVisible()) await fit.click();
      await p.getByRole("button", { name: /^Launch page/ }).first().click();
      await p.getByRole("region", { name: "The head" }).waitFor();
      await p.waitForTimeout(600);
    },
  ],
  [
    "matrix-turn",
    "?mock=still#/matrix",
    ["wide"],
    async (p) => {
      await p.getByRole("button", { name: "Fit the board" }).click();
      await p.getByRole("button", { name: /^Launch page/ }).first().click();
      await p.getByRole("button", { name: /^You: Plan a one-page launch site/ }).click();
      await p.waitForTimeout(900);
    },
  ],
  [
    "matrix-menu",
    "?mock=still#/matrix",
    ["wide"],
    async (p) => {
      await p.getByRole("button", { name: "Fit the board" }).click();
      await p.getByRole("button", { name: /^Launch page/ }).first().click();
      await p.getByRole("button", { name: "Thread options" }).click();
      await p.waitForTimeout(400);
    },
  ],
  ["matrix-board-redirect", "?mock=still#/board", ["wide"], async (p) => p.waitForURL(/#\/matrix/)],
  [
    "settings-updates",
    "?mock=still#/settings?section=updates",
    ["wide", "phone"],
    async (p) => {
      await p.locator("#updates").scrollIntoViewIfNeeded();
      await p.waitForTimeout(400);
    },
  ],
  [
    "head-renamed",
    "?mock=still#/settings/profiles/head",
    ["wide", "phone"],
    async (p) => {
      // The owner names the head's profile "Architect": the head goes by that name everywhere.
      const name = p.locator("main input").first(); // not the closed drawer's search on a phone
      await name.fill("Architect");
      await p.getByRole("button", { name: "Save changes" }).click();
      await p.waitForTimeout(500);
      await p.evaluate(() => (location.hash = "#/"));
      await p.waitForTimeout(700);
    },
  ],
  [
    "agent-attach",
    "?mock=still#/agents/hd3k9q2m7x",
    ["wide", "phone"],
    async (p) => {
      // A real PNG to attach: a crop of the page itself.
      const png = await p.screenshot({ clip: { x: 0, y: 0, width: 360, height: 240 } });
      await p.locator('input[type="file"]').setInputFiles([
        { name: "login-error.png", mimeType: "image/png", buffer: png },
        { name: "invoice-export-march.csv", mimeType: "text/csv", buffer: Buffer.from("id,amount\n1,20\n") },
      ]);
      await p.getByLabel(/^Message /).fill("The login page shows this error since this morning. Data attached.");
      await p.waitForTimeout(400);
    },
  ],
  [
    "agent-attach-sent",
    "?mock=still#/agents/hd3k9q2m7x",
    ["wide", "phone"],
    async (p) => {
      const png = await p.screenshot({ clip: { x: 0, y: 0, width: 360, height: 240 } });
      await p.locator('input[type="file"]').setInputFiles([
        { name: "login-error.png", mimeType: "image/png", buffer: png },
        { name: "invoice-export-march.csv", mimeType: "text/csv", buffer: Buffer.from("id,amount\n1,20\n") },
      ]);
      const box = p.getByLabel(/^Message /);
      await box.fill("The login page shows this error since this morning. Data attached.");
      await p.waitForFunction(() => !document.querySelector('[aria-label="Uploading…"]'));
      await box.press("Enter");
      await p.waitForTimeout(1200);
      const sc = p.getByTestId("conversation-scroll");
      await sc.evaluate((el) => (el.scrollTop = el.scrollHeight));
      await p.waitForTimeout(300);
    },
  ],
  ["project", "?mock=still#/projects/pbl7n2c4ka", ["wide", "small", "phone"]],
  ["project-paused", "?mock=still#/projects/pin2b4d6fh", ["wide"]],
  ["inbox", "?mock=still#/inbox", ["wide", "small", "phone"]],
  ["inbox-empty", "?mock=calm#/inbox", ["wide"]],
  ["settings", "?mock=still#/settings", ["wide", "phone"]],
  ["profile-reviewer", "?mock=still#/settings/profiles/reviewer", ["wide", "phone"]],
  [
    "profile-reviewer-preview",
    "?mock=still#/settings/profiles/reviewer",
    ["wide"],
    async (p) => {
      await p.getByText("Instructions", { exact: true }).scrollIntoViewIfNeeded();
      await p.waitForTimeout(300);
    },
  ],
  ["profile-lead", "?mock=still#/settings/profiles/lead", ["wide"]],
  [
    "profile-new",
    "?mock=still#/settings/profiles/new",
    ["wide", "phone"],
    async (p) => {
      await p.keyboard.type("Security auditor");
      await p.waitForTimeout(200);
    },
  ],
  ["agent-details-reviewer", "?mock=still#/agents/wk6r8t2v4x/details", ["wide", "phone"]],
  [
    "agent-details-saved",
    "?mock=still#/agents/wk2m4n6p8q/details",
    ["wide"],
    async (p) => {
      await p.locator("#cfg-role").fill("Stripe webhooks, billing state and idempotency");
      await p.getByRole("button", { name: "Save", exact: true }).click();
      await p.waitForTimeout(600);
    },
  ],
  [
    "palette",
    "?mock=still#/",
    ["wide"],
    async (p) => {
      await p.keyboard.press("Meta+k");
      await p.waitForTimeout(250);
      await p.keyboard.type("work");
      await p.waitForTimeout(250);
    },
  ],
  [
    "new-project",
    "?mock=still#/",
    ["wide", "phone"],
    async (p) => {
      await p.keyboard.press("c");
      await p.waitForTimeout(250);
      await p.keyboard.type("Billing reports");
      await p.waitForTimeout(150);
    },
  ],
  [
    "shortcuts",
    "?mock=still#/",
    ["wide"],
    async (p) => {
      await p.keyboard.press("Shift+?");
      await p.waitForTimeout(250);
    },
  ],
  [
    "phone-nav",
    "?mock=still#/",
    ["phone"],
    async (p) => {
      await p.getByLabel("Open navigation").click();
      await p.waitForTimeout(400);
    },
  ],
  [
    "delete-project",
    "?mock=still#/projects/pdc5v7x9za",
    ["wide"],
    async (p) => {
      await p.getByLabel("Project actions").click();
      await p.getByText("Delete project…").click();
      await p.waitForTimeout(300);
    },
  ],
  // Safety: a guard hold in Needs you and in the conversation, a blocked command, Settings → Safety,
  // and "Confirm on this Mac" while the (simulated) owner gate waits.
  ["safety-inbox", "?mock=still#/inbox?id=apg8h2k4m6", ["wide", "phone"]],
  ["safety-agent", "?mock=still#/agents/ld9w3e5r7t", ["wide"]],
  [
    "safety-settings",
    "?mock=still#/settings",
    ["wide"],
    async (p) => {
      await p.getByText("Kill processes by name").first().click();
      await p.waitForTimeout(250);
      await p.getByText("Dangerous-command guard", { exact: true }).scrollIntoViewIfNeeded();
      await p.waitForTimeout(250);
    },
  ],
  [
    "safety-gate",
    "?mock=still#/settings",
    ["wide"],
    async (p) => {
      await p.getByText("Will ask before").scrollIntoViewIfNeeded();
      await p.waitForTimeout(250);
    },
  ],
  [
    "safety-confirm",
    "?mock=gate-wait#/agents/ld9w3e5r7t",
    ["wide"],
    async (p) => {
      await p.getByRole("button", { name: "Allow once" }).click();
      await p.waitForTimeout(700);
    },
  ],
  // Harnesses: the New agent dialog, a terminal agent's menu as an approval, Settings → Harnesses
  // and Running outside tmx, the adopt dialog.
  [
    "harness-new-agent",
    "?mock=still#/projects/pbl7n2c4ka",
    ["wide", "phone"],
    async (p) => {
      await p.getByRole("button", { name: "New agent" }).first().click();
      await p.waitForTimeout(400);
      await p.keyboard.type("api-tests");
      await p.getByRole("radio", { name: /^Codex/ }).click();
      await p.waitForTimeout(250);
    },
  ],
  ["harness-menu-inbox", "?mock=still#/inbox?id=apt5y7u9i1", ["wide", "phone"]],
  ["harness-menu-agent", "?mock=still#/agents/oc3k5m7p9r/conversation", ["wide"]],
  [
    "harness-discover",
    "?mock=still#/settings",
    ["wide"],
    async (p) => {
      await p.getByText("Running outside tmx", { exact: true }).scrollIntoViewIfNeeded();
      await p.waitForTimeout(600);
    },
  ],
  [
    "harness-adopt",
    "?mock=still#/settings",
    ["wide"],
    async (p) => {
      await p.getByRole("button", { name: "Adopt…" }).nth(1).click();
      await p.waitForTimeout(400);
    },
  ],
  ["chat-dm", "?mock=still#/chats/dm-5f2a91c4be07d3a8", ["wide", "phone"]],
  ["chat-group", "?mock=still#/chats/g8fk2m4q7xz", ["wide", "small", "phone"]],
  ["chats-home", "?mock=still#/chats", ["wide"]],
  [
    "chat-mention",
    "?mock=still#/chats/g8fk2m4q7xz",
    ["wide", "phone"],
    async (p) => {
      const box = p.getByLabel(/^Message Weekend trip/);
      await box.click();
      await box.type("Sure, I'll check. @pri");
      await p.waitForSelector('[role="listbox"]');
      await p.waitForTimeout(250);
    },
  ],
  [
    "chat-attach-sent",
    "?mock=still#/chats/g8fk2m4q7xz",
    ["wide"],
    async (p) => {
      const png = await p.screenshot({ clip: { x: 0, y: 0, width: 360, height: 240 } });
      await p.locator('input[type="file"]').setInputFiles([
        { name: "train-times.png", mimeType: "image/png", buffer: png },
        { name: "packing-list.txt", mimeType: "text/plain", buffer: Buffer.from("boots\njacket\n") },
      ]);
      const box = p.getByLabel(/^Message Weekend trip/);
      await box.fill("Here are the Sunday trains @Alex Weber ");
      await p.waitForFunction(() => !document.querySelector('[aria-label="Uploading…"]'));
      await box.press("Escape");
      await box.press("Enter");
      await p.waitForTimeout(900);
      const sc = p.getByTestId("chat-scroll");
      await sc.evaluate((el) => (el.scrollTop = el.scrollHeight));
      await p.waitForTimeout(300);
    },
  ],
  ["settings-chat-name", "?mock=still#/settings?section=machine", ["wide"], async (p) => {
    await p.getByLabel("Name in chats").scrollIntoViewIfNeeded();
    await p.waitForTimeout(300);
  }],
  ["chats-empty", "?mock=empty#/chats", ["wide"]],
  // chat boards (DESIGN.md "Chat boards")
  ["chat-board", "?mock=still#/chats/g8fk2m4q7xz/board", ["wide", "small", "phone"], async (p) => p.waitForTimeout(600)],
  [
    "chat-board-card",
    "?mock=still#/chats/g8fk2m4q7xz/board",
    ["wide", "phone"],
    async (p) => {
      await p.getByRole("button", { name: /^Alex Weber: Casa Mulin/ }).click();
      await p.waitForTimeout(700);
    },
  ],
  [
    "chat-board-log",
    "?mock=still#/chats/g8fk2m4q7xz/board",
    ["wide"],
    async (p) => {
      await p.getByRole("button", { name: /^Getting there: open the log/ }).click();
      await p.waitForTimeout(500);
    },
  ],
  [
    "chat-board-menu",
    "?mock=still#/chats/g8fk2m4q7xz/board",
    ["wide"],
    async (p) => {
      await p.getByRole("button", { name: "Where we stay options" }).click();
      await p.getByRole("menuitem", { name: "Colour" }).hover();
      await p.waitForTimeout(400);
    },
  ],
  ["chat-board-dm", "?mock=still#/chats/dm-5f2a91c4be07d3a8/board", ["wide", "phone"]],
  [
    "chat-topic-picker",
    "?mock=still#/chats/g8fk2m4q7xz",
    ["wide"],
    async (p) => {
      await p.getByRole("button", { name: /^Topic: / }).click();
      await p.waitForTimeout(400);
    },
  ],
  [
    "chat-invite",
    "?mock=still#/chats",
    ["wide", "phone"],
    async (p) => {
      await p.getByRole("button", { name: "Invite someone" }).first().click();
      await p.waitForTimeout(900);
    },
  ],
  [
    "chat-join",
    "?mock=still#/chats",
    ["wide"],
    async (p) => {
      await p.getByRole("button", { name: "Join" }).first().click();
      await p.getByLabel("Invite link").fill("tmx://join/group-eyJ2IjoxfQ");
      await p.waitForTimeout(900);
    },
  ],
  // Handing a group to another member: the member menu, the dialog (Alex; Priya's tmx is older),
  // the owner's confirmation, waiting for Priya's offline tmx, Alex taking over, the admin's
  // Leave dialog and its "Make someone admin first".
  ["chat-admin-menu", "?mock=still#/chats/g8fk2m4q7xz", ["wide", "phone"], async (p) => {
    await openMembers(p);
    await p.getByRole("button", { name: "Alex Weber options" }).click();
    await p.waitForTimeout(300);
  }],
  ["chat-admin-dialog", "?mock=still#/chats/g8fk2m4q7xz", ["wide", "phone"], async (p) => {
    await makeAdmin(p, "Alex Weber");
  }],
  ["chat-admin-confirm", "?mock=still#/chats/g8fk2m4q7xz", ["wide"], async (p) => {
    await makeAdmin(p, "Alex Weber");
    await p.getByRole("button", { name: "Make admin", exact: true }).click();
    await p.waitForTimeout(600);
  }],
  ["chat-admin-waiting", "?mock=still#/chats/g8fk2m4q7xz", ["wide", "phone"], async (p) => {
    await makeAdmin(p, "Priya Nair");
    await p.getByRole("button", { name: "Make admin", exact: true }).click();
    await p.waitForSelector("[data-admin-offer]", { timeout: 8000 });
    await p.waitForTimeout(300);
  }],
  ["chat-admin-done", "?mock=still#/chats/g8fk2m4q7xz", ["wide"], async (p) => {
    await makeAdmin(p, "Alex Weber");
    await p.getByRole("button", { name: "Make admin", exact: true }).click();
    await p.waitForSelector("text=You made Alex Weber the admin.", { timeout: 9000 });
    await p.waitForTimeout(400);
  }],
  ["chat-admin-leave", "?mock=still#/chats/g8fk2m4q7xz", ["wide", "phone"], async (p) => {
    await openMembers(p);
    await p.getByRole("button", { name: "Leave the group…" }).click();
    await p.waitForTimeout(400);
  }],
  ["chat-admin-pick", "?mock=still#/chats/g8fk2m4q7xz", ["wide"], async (p) => {
    await openMembers(p);
    await p.getByRole("button", { name: "Leave the group…" }).click();
    await p.getByRole("button", { name: "Make someone admin first" }).click();
    await p.waitForTimeout(500);
  }],
  // Companion: Settings → Companion (connected, and nothing set up), an agent's hand-off and
  // Companion's question in Needs you, the Hand to Companion dialog and its preview, the
  // Overview strip.
  [
    "companion-settings",
    "?mock=still#/settings?section=companion",
    ["wide", "phone"],
    async (p) => {
      await p.locator("[data-companion-task]").first().locator("button").first().click();
      await p.waitForTimeout(300);
      await p.locator("#companion").scrollIntoViewIfNeeded();
      await p.waitForTimeout(300);
    },
  ],
  ["companion-settings-off", "?mock=companion-off#/settings?section=companion", ["wide"], async (p) => p.waitForTimeout(400)],
  ["companion-settings-key-refused", "?mock=companion-key-refused#/settings?section=companion", ["wide", "phone"], async (p) => p.waitForTimeout(400)],
  ["companion-settings-missing", "?mock=companion-missing#/settings?section=companion", ["wide"], async (p) => p.waitForTimeout(400)],
  // One click: Connect with Companion opens Companion's "Connect tmx?"; the mock's Companion hands
  // tmx its key ~3.5 s later (the owner's click there): waiting, then Connected with a toast.
  ["companion-connect", "?mock=companion-connect#/settings?section=companion", ["wide", "phone"], async (p) => p.waitForTimeout(400)],
  [
    "companion-connect-waiting",
    "?mock=companion-connect#/settings?section=companion",
    ["wide", "phone"],
    async (p) => {
      await p.getByRole("button", { name: "Connect with Companion" }).click();
      await p.waitForSelector("[data-companion-connecting]");
      await p.waitForTimeout(600);
    },
  ],
  [
    "companion-connect-done",
    "?mock=companion-connect#/settings?section=companion",
    ["wide"],
    async (p) => {
      await p.getByRole("button", { name: "Connect with Companion" }).click();
      await p.waitForSelector("[data-companion-connecting]");
      await p.waitForSelector("[data-companion-connecting]", { state: "detached", timeout: 10000 });
      await p.waitForTimeout(500);
    },
  ],
  [
    "companion-connect-paste",
    "?mock=companion-connect#/settings?section=companion",
    ["wide"],
    async (p) => {
      await p.getByRole("button", { name: "Paste a key instead" }).click();
      await p.waitForTimeout(300);
    },
  ],
  ["companion-inbox-handoff", "?mock=still#/inbox?id=apct4w6y8z", ["wide", "phone"]],
  ["companion-inbox-question", "?mock=still#/inbox?id=apcq3e5r7t", ["wide"]],
  ["companion-inbox-hand", "?mock=still#/inbox?id=apq7w2e9r4", ["wide"]],
  [
    "companion-dialog",
    "?mock=still#/agents/hd3k9q2m7x",
    ["wide", "phone"],
    async (p) => {
      await p.getByRole("button", { name: "Hand to Companion" }).first().click();
      await p.waitForTimeout(300);
      await p.locator("#ho-goal").fill("Check whether the staging SSL certificate of billing-portal.dev is still valid");
      await p.locator("#ho-steps").fill("Open https://staging.billing-portal.dev in the browser\nRead the certificate's expiry date");
      await p.locator("#ho-success").fill("You report the expiry date");
      await p.locator("#ho-dont").fill("Do not log in anywhere");
      await p.waitForTimeout(200);
    },
  ],
  [
    "companion-preview",
    "?mock=still#/agents/hd3k9q2m7x",
    ["wide", "phone"],
    async (p) => {
      await p.getByRole("button", { name: "Hand to Companion" }).first().click();
      await p.waitForTimeout(300);
      await p.locator("#ho-goal").fill("Check whether the staging SSL certificate of billing-portal.dev is still valid");
      await p.locator("#ho-steps").fill("Open https://staging.billing-portal.dev in the browser\nRead the certificate's expiry date");
      await p.locator("#ho-dont").fill("Do not log in anywhere");
      await p.getByRole("button", { name: "Preview" }).click();
      await p.waitForSelector("[data-companion-preview]");
      await p.waitForTimeout(300);
    },
  ],
  ["companion-overview", "?mock=still#/", ["wide"], async (p) => p.getByLabel("Companion tasks").scrollIntoViewIfNeeded()],
  // Companion 1:1 (#/companion): the conversation (the owner's and the head's messages, replies,
  // a task card, a waiting question, an error note, a message in flight), answering a question,
  // a message going out and Companion's reply, not connected, nothing yet, and the sidebar's
  // Companion item (unread dot, busy spinner; counted in the collapsed Chats badge).
  ["companion-chat", "?mock=companion-chat#/companion", ["wide", "small", "phone"], async (p) => p.waitForSelector("[data-entry]")],
  [
    "companion-chat-answer",
    "?mock=companion-chat#/companion",
    ["wide", "phone"],
    async (p) => {
      await p.getByRole("button", { name: "Answer", exact: true }).click();
      await p.getByLabel("Message Companion").fill("The staging one");
      await p.waitForTimeout(300);
    },
  ],
  [
    "companion-chat-sending",
    "?mock=companion-chat#/companion",
    ["wide"],
    async (p) => {
      const box = p.getByLabel("Message Companion");
      await box.fill("Is anything on my calendar tomorrow morning?");
      await box.press("Enter");
      await p.waitForTimeout(1300);
    },
  ],
  [
    "companion-chat-replied",
    "?mock=companion-chat#/companion",
    ["wide", "phone"],
    async (p) => {
      await p.getByRole("button", { name: "Answer", exact: true }).click();
      const box = p.getByLabel("Message Companion");
      await box.fill("The staging one");
      await box.press("Enter");
      await p.waitForTimeout(800);
      await box.fill("Is anything on my calendar tomorrow morning?");
      await box.press("Enter");
      await p.waitForSelector("text=That endpoint's signing secret", { timeout: 8000 });
      await p.waitForTimeout(3200);
    },
  ],
  ["companion-chat-off", "?mock=companion-chat-off#/companion", ["wide", "phone"], async (p) => p.waitForSelector("[data-entry]")],
  ["companion-chat-empty", "?mock=calm#/companion", ["wide", "phone"], async (p) => p.waitForSelector("[data-companion-empty]")],
  ["companion-sidebar", "?mock=companion-chat#/", ["wide"], async (p) => p.waitForSelector("[data-companion-item]")],
  [
    "companion-sidebar-collapsed",
    "?mock=companion-chat#/",
    ["wide"],
    async (p) => {
      await p.waitForSelector("[data-companion-item]");
      await p.getByRole("button", { name: "Chats", exact: true }).click();
      await p.waitForTimeout(200);
    },
  ],
  [
    "companion-sidebar-nav",
    "?mock=companion-chat#/",
    ["phone"],
    async (p) => {
      await p.getByLabel("Open navigation").click();
      await p.waitForSelector("[role=dialog] [data-companion-item]");
      await p.waitForTimeout(400);
    },
  ],
];

// The chat's members: a side panel on wide screens, a sheet behind the Members button otherwise.
async function openMembers(p) {
  if ((p.viewportSize()?.width ?? 0) >= 1280) return;
  await p.getByRole("button", { name: "Members", exact: true }).click();
  await p.waitForTimeout(400);
}

async function makeAdmin(p, name) {
  await openMembers(p);
  await p.getByRole("button", { name: `${name} options` }).click();
  await p.getByRole("menuitem", { name: /Make admin/ }).click();
  await p.waitForTimeout(450);
}

const browser = await chromium.launch({
  headless: true,
  args: ["--disable-features=WebAuthenticationUseNativeWinApi"],
});
const errors = [];
try {
  for (const text of (process.env.SHOT_TEXT ?? "normal").split(",")) {
  for (const theme of (process.env.SHOT_THEMES ?? "light,dark").split(",")) {
    for (const [name, url, sizes, setup] of SHOTS) {
      for (const size of sizes) {
        const id = `${name}-${size}-${theme}` + (text === "normal" ? "" : `-${text}`);
        if (filter && !id.includes(filter)) continue;
        const ctx = await browser.newContext({
          viewport: SIZES[size],
          deviceScaleFactor: 2,
          colorScheme: theme,
          isMobile: size === "phone",
          hasTouch: size === "phone",
        });
        const page = await ctx.newPage();
        const cdp = await ctx.newCDPSession(page);
        await cdp.send("WebAuthn.enable");
        await cdp.send("WebAuthn.addVirtualAuthenticator", {
          options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true },
        });
        page.on("pageerror", (e) => errors.push(`${id}: ${e.message}`));
        page.on("console", (m) => m.type() === "error" && errors.push(`${id}: console: ${m.text()}`));
        await page.addInitScript((t) => localStorage.setItem("tmx.textSize", t), text); // lib/text-size.ts
        await page.goto(base + url);
        await page.waitForSelector("main", { timeout: 10000 });
        await page.waitForTimeout(900);
        try {
          if (setup) await setup(page);
        } catch (e) {
          // keep what the page showed when a step failed, and go on with the other shots
          await page.screenshot({ path: path.join(out, `${id}-FAILED.png`) });
          errors.push(`${id}: ${e.message.split("\n")[0]}`);
          console.log("FAILED", id);
          await ctx.close();
          continue;
        }
        await page.screenshot({ path: path.join(out, `${id}.png`) });
        const over = await overflowOf(page);
        if (over) errors.push(`${id}: overflow: ${over}`);
        console.log("shot", id);
        await ctx.close();
      }
    }
  }
  }
} finally {
  await browser.close();
  server.close();
}
if (errors.length) {
  console.log("\nPage errors:\n" + errors.join("\n"));
  process.exitCode = 1;
}
