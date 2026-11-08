// Public Linux CI only, using the existing throwaway daemon/browser harness.
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { launchBrowser, openPage, startDaemon } from "./harness.mjs";

test("phone banner visibility, Later, denied permission, and Living cell screenshots", { timeout: 120_000 }, async t => {
  const d = await startDaemon({ phone: true });
  t.after(() => d.stop());
  const browser = await launchBrowser();
  t.after(() => browser.close());
  const { page, errors } = await openPage(browser, { phone: true, origin: d.proxy.origin });
  let enabled = false;
  await page.route("**/api/push/key", route => route.fulfill({ json: { public_key: "BA", enabled } }));
  // Browser push service is external to CI. Reconcile a deterministic local subscription;
  // denied permission checks the real shared flow without contacting a push service.
  await page.addInitScript(() => {
    Object.defineProperty(Notification, "permission", { get: () => "granted" });
    Notification.requestPermission = async () => "denied";
    navigator.serviceWorker.getRegistration = async () => ({ pushManager: { getSubscription: async () => ({}) } });
  });
  await page.goto(d.ownerURL().replace(d.url, d.proxy.origin));
  await page.waitForFunction(() => !location.hash.includes("token="));
  const banner = page.getByRole("region", { name: "Phone notifications", exact: true });
  await banner.waitFor();
  assert.equal((await banner.boundingBox()).y, 0, "banner is at the top of the first screen");
  for (const el of [banner.getByText("Get approvals on this phone"), banner.getByRole("button", { name: "Enable", exact: true }), banner.getByRole("button", { name: "Later" })]) {
    assert.ok(await el.evaluate(e => parseFloat(getComputedStyle(e).fontSize)) >= 18);
  }
  mkdirSync("screenshots", { recursive: true });
  await page.screenshot({ path: "screenshots/phone-notifications-390x844.png", scale: "css" });
  // Keep a persisted desktop sidebar width from stealing the phone's content area.
  await page.evaluate(() => localStorage.setItem("tmx.sidebar.width", "380"));
  await page.goto(`${d.proxy.origin}/#/settings`);
  await page.locator("#settings-section").waitFor();
  const settings = page.locator(".settings-page");
  const enable = settings.getByRole("button", { name: "Enable notifications", exact: true });
  await enable.waitFor();
  const box = await enable.boundingBox();
  assert.ok(box.x >= 0 && box.x + box.width <= 390 && box.y >= 0 && box.y + box.height <= 844, "Enable notifications fits on the first Settings screen");
  assert.ok(box.height >= 44, "Enable notifications is a phone-sized tap target");
  assert.equal(await page.locator("aside").first().isVisible(), false, "desktop navigation collapses on phone");
  assert.ok((await settings.boundingBox()).width >= 389, "Settings gets the full phone width");
  await page.screenshot({ path: "screenshots/settings-phone-notifications-390x844.png", scale: "css" });
  for (const id of ["safety", "appearance"]) {
    await page.getByLabel("Settings section", { exact: true }).selectOption(id);
    await page.locator(`#${id}`).waitFor();
    await page.screenshot({ path: `screenshots/settings-${id}-390x844.png`, scale: "css" });
  }
  // Scan ALL rendered settings panes, not just the three photographed ones.
  for (const pane of await settings.locator("section").all()) {
    await pane.scrollIntoViewIfNeeded();
    const problems = await pane.evaluate(root => {
      const bad = [];
      // Temporary CI diagnostics: identify the actual text node and every layout
      // ancestor, including duplicate rule names in the Safety Changes log.
      const flag = (el, reason) => {
        const ancestors = [];
        for (let node = el; node; node = node.parentElement) {
          const style = getComputedStyle(node);
          ancestors.push({
            tagName: node.tagName, className: node.className,
            width: node.getBoundingClientRect().width,
            clientWidth: node.clientWidth, scrollWidth: node.scrollWidth,
            display: style.display, visibility: style.visibility,
            overflowX: style.overflowX, flex: style.flex,
            gridTemplateColumns: style.gridTemplateColumns,
          });
        }
        bad.push({ reason, ancestors });
      };
      for (const el of [root, ...root.querySelectorAll("*")]) {
        if (!(el instanceof HTMLElement) || !el.getClientRects().length) continue;
        const style = getComputedStyle(el);
        if (style.position === "absolute" || el.closest(".sr-only")) continue;
        if (el.clientWidth && el.scrollWidth > el.clientWidth + 1 && style.overflowX !== "hidden") flag(el, `overflow: ${el.tagName} ${el.scrollWidth}/${el.clientWidth}`);
        const text = [...el.childNodes].filter(n => n.nodeType === Node.TEXT_NODE).map(n => n.textContent).join("").trim();
        // Short units, icons and badges may naturally be narrow; prose must have
        // at least 120px so a fixed control cannot squeeze it one letter per line.
        if (text.length > 24) {
          const width = style.display === "inline" ? el.parentElement.getBoundingClientRect().width : el.getBoundingClientRect().width;
          if (width < 120) flag(el, `squeezed (${width}px): ${text.slice(0, 60)}`);
          if (parseFloat(style.fontSize) < 18) flag(el, `small text: ${text.slice(0, 60)}`);
        }
      }
      return bad;
    });
    if (problems.length) console.error("Settings layout diagnostics:", JSON.stringify(problems, null, 2));
    assert.deepEqual(problems, [], `Settings pane: ${await pane.locator("h2").allTextContents()}`);
  }
  assert.equal(await page.evaluate(() => [document.documentElement, document.body, document.querySelector("main"), document.querySelector(".settings-page .scroll-thin")].some(el => el.scrollWidth > el.clientWidth + 1)), false, "no horizontal page or Settings scroll");
  await page.goto(`${d.proxy.origin}/#/`);
  const logo = page.locator('main img[alt="Tiamatrix"]');
  assert.equal(await logo.getAttribute("src"), "/living-cell.svg");
  await logo.screenshot({ path: "screenshots/living-cell-header.png", scale: "css" });
  await banner.getByRole("button", { name: "Later" }).click();
  await banner.waitFor({ state: "detached" });
  await page.reload();
  await banner.waitFor();
  await banner.getByRole("button", { name: "Enable", exact: true }).click();
  await banner.getByRole("status").filter({ hasText: "Allow notifications" }).waitFor();
  enabled = true;
  await Promise.all([page.waitForResponse("**/api/push/key"), page.reload()]);
  await banner.waitFor({ state: "detached" });
  await page.setViewportSize({ width: 1280, height: 900 });
  enabled = false;
  await Promise.all([page.waitForResponse("**/api/push/key"), page.reload()]);
  assert.equal(await banner.isVisible(), false, "phone prompt hidden on desktop");
  assert.deepEqual(errors, []);
  await page.setContent(`<img src="${d.proxy.origin}/living-cell.svg" width="256" height="256" alt="Living cell">`);
  await page.getByAltText("Living cell").screenshot({ path: "screenshots/living-cell-logo.png", scale: "css" });
});
