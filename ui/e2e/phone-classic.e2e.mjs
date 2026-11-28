// The classic UI profile on a phone (config.json "ui": {"profile": "classic"}): the Python
// dashboard's mobile app, one to one (the owner, 17 Nov 2026). At 390x844: nothing fixed above a
// page, a 76 px bottom bar with Home, Projects, Agents and Your turn, a compact agent header
// and a back arrow, Home's plain rows instead of the menu, no notifications banner.
// Linux CI only, like the other browser tests.
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { launchBrowser, openPage, signIn, startDaemon } from "./harness.mjs";

const overflow = page => page.evaluate(() =>
  [document.documentElement, document.body, ...document.querySelectorAll("main, main > *, main > * > .scroll-thin")]
    .filter(el => el.clientWidth && el.scrollWidth > el.clientWidth + 1)
    .map(el => `${el.tagName}.${el.className}: ${el.scrollWidth} > ${el.clientWidth}`));

test("classic profile at phone size: Python's mobile app", { timeout: 5 * 60_000 }, async t => {
  const d = await startDaemon({ config: { ui: { profile: "classic" } } });
  t.after(() => d.stop());
  const browser = await launchBrowser();
  t.after(() => browser.close());
  try {
    const { page, errors } = await openPage(browser, { phone: true, origin: d.url });
    await signIn(page, d);
    mkdirSync("screenshots", { recursive: true });

    // Home: no top bar, no menu button, no banner; the page starts at the top of the screen.
    await page.locator("[data-phone-home]").waitFor();
    assert.equal(await page.getByRole("button", { name: "Open navigation" }).count(), 0, "no menu button");
    assert.equal(await page.getByRole("region", { name: "Phone notifications", exact: true }).count(), 0, "no notifications banner");
    assert.ok((await page.locator("[data-phone-home]").boundingBox()).y <= 1, "nothing is fixed above Home");
    assert.ok(await page.getByRole("heading", { level: 1 }).first().isVisible(), "Home opens on its headline");
    assert.deepEqual(await overflow(page), [], "Home: no horizontal scroll");
    await page.screenshot({ path: "screenshots/classic-phone-home-390x844.png", scale: "css" });

    // The bottom bar: four tabs, 76 px, at the foot of the screen, each a thumb target.
    const bar = page.getByRole("navigation", { name: "Main navigation" });
    assert.equal(await bar.getByRole("link").count(), 4, "four tabs");
    const barBox = await bar.boundingBox();
    assert.ok(Math.abs(barBox.height - 76) <= 1, `the bar is 76 px: ${JSON.stringify(barBox)}`);
    assert.ok(Math.abs(barBox.y + barBox.height - 844) <= 1, `the bar ends at the foot of the screen: ${JSON.stringify(barBox)}`);
    assert.equal(await bar.getByRole("link", { name: "Home", exact: true }).getAttribute("aria-current"), "page");
    for (const name of ["Projects", "Agents"]) {
      const tab = bar.getByRole("link", { name, exact: true });
      const box = await tab.boundingBox();
      assert.ok(box.height >= 48 && box.width >= 48, `${name} is a thumb target: ${JSON.stringify(box)}`);
      assert.ok(await tab.evaluate(el => parseFloat(getComputedStyle(el).fontSize)) >= 12, `${name}: the label is at least 12 px`);
      await tab.click();
      const headline = page.locator("[data-phone-headline] h1");
      await headline.filter({ hasText: name }).waitFor();
      assert.equal(await tab.getAttribute("aria-current"), "page", `${name} is lit`);
      assert.ok((await headline.boundingBox()).y < 66, `${name} opens on its headline, with nothing fixed above`);
      assert.equal(await page.locator("[data-phone-title-row]").count(), 0, `${name}: no title row`);
      assert.deepEqual(await overflow(page), [], `${name}: no horizontal scroll`);
    }
    await page.screenshot({ path: "screenshots/classic-phone-agents-390x844.png", scale: "css" });

    // Agents: a search field and 66 px rows; a row opens the chat, whose one row has a back arrow.
    const rows = page.getByRole("list", { name: "Agents" }).getByRole("link");
    if (await rows.count()) {
      assert.ok((await rows.first().boundingBox()).height >= 66, "agent rows are 66 px");
      await rows.first().click();
      const titleRow = page.locator("[data-phone-title-row]");
      await titleRow.waitFor();
      const box = await titleRow.boundingBox();
      assert.ok(box.y <= 1 && box.height >= 48 && box.height <= 64, `one compact row above the chat: ${JSON.stringify(box)}`);
      const name = await titleRow.getByRole("heading", { level: 1 }).boundingBox();
      const state = await titleRow.locator("[data-phone-agent-line]").boundingBox();
      assert.ok(name && state && Math.abs((name.y + name.height / 2) - (state.y + state.height / 2)) <= 2,
        "the agent name and state share one line");
      assert.equal(await bar.getByRole("link", { name: "Agents", exact: true }).getAttribute("aria-current"), "page", "an agent stays under Agents");
      await page.screenshot({ path: "screenshots/classic-phone-agent-390x844.png", scale: "css" });
      const back = titleRow.getByRole("link", { name: "Back", exact: true });
      const backBox = await back.boundingBox();
      assert.ok(backBox.width >= 44 && backBox.height >= 44, "the back arrow is a thumb target");
      await back.click();
      await page.waitForFunction(() => location.hash === "#/agents");
    }

    // Your turn: its headline, and (nothing waits in a fresh daemon) the empty state.
    await bar.getByRole("link", { name: /^Your turn/ }).click();
    await page.locator("[data-turn-deck]").waitFor();
    await page.locator("[data-phone-headline] h1").filter({ hasText: "Your turn" }).waitFor();
    assert.deepEqual(await overflow(page), [], "Your turn: no horizontal scroll");
    await page.screenshot({ path: "screenshots/classic-phone-your-turn-390x844.png", scale: "css" });

    // Home's plain rows replace the menu: each 48 px, and "Notifications" opens the push setting.
    await bar.getByRole("link", { name: "Home", exact: true }).click();
    const more = page.getByRole("navigation", { name: "More" });
    await more.waitFor();
    const labels = (await more.getByRole("link").allInnerTexts()).map(s => s.split("\n")[0].trim());
    for (const label of ["Notifications", "WhatsApp", "Recordings", "Matrix", "People", "Usage and accounts", "Settings"]) {
      assert.ok(labels.some(l => l.startsWith(label)), `Home has a row for ${label}: ${labels.join(" | ")}`);
    }
    for (const row of await more.getByRole("link").all()) assert.ok((await row.boundingBox()).height >= 48, "plain rows are 48 px");
    await more.getByRole("link", { name: /^Notifications/ }).click();
    await page.locator("#phone-notifications").waitFor();
    const settingsRow = page.locator("[data-phone-title-row]");
    await settingsRow.waitFor();
    await settingsRow.getByRole("link", { name: "Back", exact: true }).click();
    await page.locator("[data-phone-home]").waitFor();

    // A reload keeps the session and the profile.
    await page.reload();
    await page.locator("[data-phone-home]").waitFor();
    assert.equal(await bar.getByRole("link").count(), 4);
    assert.deepEqual(errors, [], "no page errors");
  } catch (e) {
    console.log("daemon log (tail):\n" + d.log.slice(-40).join(""));
    throw e;
  }
});

test("classic profile on a computer: no phone bar", { timeout: 2 * 60_000 }, async t => {
  const d = await startDaemon({ config: { ui: { profile: "classic" } } });
  t.after(() => d.stop());
  const browser = await launchBrowser();
  t.after(() => browser.close());
  try {
    const { page, errors } = await openPage(browser, { origin: d.url });
    await signIn(page, d);
    await page.locator("aside").first().waitFor();
    assert.equal(await page.getByRole("navigation", { name: "Main navigation" }).count(), 0, "the bottom bar is a phone's");
    assert.equal(await page.locator("[data-phone-home]").count(), 0);
    assert.deepEqual(errors, [], "no page errors");
  } catch (e) {
    console.log("daemon log (tail):\n" + d.log.slice(-40).join(""));
    throw e;
  }
});
