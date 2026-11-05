// Linux CI only, using the existing throwaway daemon/browser harness.
import { test } from "node:test";
import assert from "node:assert/strict";
import { launchBrowser, openPage, signIn, startDaemon } from "./harness.mjs";

test("message pins sync between desktop and phone and retain done items", { timeout: 90_000 }, async () => {
  const d = await startDaemon();
  const browser = await launchBrowser();
  try {
    const desktop = await openPage(browser);
    const phone = await openPage(browser, { phone: true });
    await signIn(desktop.page, d);
    await signIn(phone.page, d);
    const text = "Check the unfinished instruction before closing today.";
    await d.api("POST", "/api/message-pins", { source: "test:1", label: "Head conversation", href: "#/matrix", text, message_at: Date.now() });
    for (const { page } of [desktop, phone]) {
      const strip = page.getByRole("region", { name: "Pinned messages" });
      await strip.getByText(text, { exact: true }).waitFor();
      const font = await strip.getByText(text, { exact: true }).evaluate(e => parseFloat(getComputedStyle(e).fontSize));
      assert.ok(font >= 20, `pin text is large: ${font}`);
      const button = await strip.getByRole("button", { name: "Mark done", exact: true }).boundingBox();
      assert.ok(button.height >= 44 && button.width >= 44, "touch target");
      await page.evaluate(() => { location.hash = "/settings"; });
      assert.ok(await strip.isVisible(), "strip survives navigation");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "no page overflow");
    }
    await phone.page.getByRole("button", { name: "Mark done", exact: true }).click();
    await desktop.page.getByRole("heading", { name: "Pinned · 0 unfinished", exact: true }).waitFor();
    await desktop.page.getByRole("button", { name: "Done (1)", exact: true }).click();
    await desktop.page.getByText(text, { exact: true }).waitFor();
    await desktop.page.reload();
    await desktop.page.getByRole("button", { name: "Done (1)", exact: true }).click();
    await desktop.page.getByRole("button", { name: "Reopen", exact: true }).click();
    await phone.page.getByRole("heading", { name: "Pinned · 1 unfinished", exact: true }).waitFor();
    await phone.page.getByRole("button", { name: "Unpin", exact: true }).click();
    await desktop.page.getByRole("heading", { name: "Pinned · 0 unfinished", exact: true }).waitFor();
    assert.deepEqual([...desktop.errors, ...phone.errors], []);
  } finally { await browser.close(); await d.stop(); }
});

test("one tap pins and unpins a conversation message on desktop and phone", { timeout: 90_000 }, async () => {
  const d = await startDaemon();
  const browser = await launchBrowser();
  try {
    for (const phone of [false, true]) {
      const { page, context, errors } = await openPage(browser, { phone });
      await page.goto(`${d.url}/?mock=still#/agents/hd3k9q2m7x/conversation`);
      const pin = page.getByRole("button", { name: "Pin message", exact: true }).first();
      await pin.click();
      await page.getByRole("heading", { name: "Pinned · 1 unfinished", exact: true }).waitFor();
      await page.getByRole("button", { name: "Unpin message", exact: true }).first().click();
      await page.getByRole("heading", { name: "Pinned · 0 unfinished", exact: true }).waitFor();
      assert.deepEqual(errors, []);
      await context.close();
    }
  } finally { await browser.close(); await d.stop(); }
});
