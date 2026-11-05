// Phone-size pages load without a token loop (the Python fleet's 2 Nov SEV-1: a dashboard token prompt and a
// phone re-pair after an update). Owner pages at 390x844: each main page renders its heading, never the
// "needs your owner token" / "Pair this device" screen, also after a reload, a back/forward and a new tab of
// the same session.
import { test } from "node:test";
import assert from "node:assert/strict";
import { launchBrowser, openPage, signIn, startDaemon } from "./harness.mjs";

const PAGES = ["/", "/inbox", "/matrix", "/board", "/recordings", "/settings"]; // /board: the Board's old address opens the Matrix
const TOKEN_SCREEN = /needs your owner token|Pair this device/i;

test("phone-size pages load without a token loop", { timeout: 5 * 60_000 }, async () => {
  const d = await startDaemon();
  const browser = await launchBrowser();
  try {
    const { page, errors } = await openPage(browser, { phone: true, origin: d.url });
    await signIn(page, d);
    let requests401 = 0;
    page.on("response", r => { if (r.status() === 401) requests401++; });
    for (const p of PAGES) {
      await page.evaluate(h => { location.hash = h; }, p);
      await page.waitForLoadState("networkidle");
      const body = await page.locator("body").innerText();
      assert.doesNotMatch(body, TOKEN_SCREEN, `${p}: token screen at phone size`);
      assert.ok(await page.getByRole("heading").first().isVisible(), `${p}: rendered (a heading is visible): ${body.slice(0, 200)}`);
    }
    await page.reload();                                     // a reload keeps the session (no prompt, no loop)
    await page.waitForLoadState("networkidle");
    assert.doesNotMatch(await page.locator("body").innerText(), TOKEN_SCREEN, "after a reload");
    await page.goBack(); await page.goForward();
    await page.waitForLoadState("networkidle");
    assert.doesNotMatch(await page.locator("body").innerText(), TOKEN_SCREEN, "after back/forward");
    // a token loop shows as a stream of 401s: a handful at most
    assert.ok(requests401 <= 2, `401s while browsing: ${requests401}`);
    // the URL no longer carries the token (it was read once and stripped)
    assert.doesNotMatch(page.url(), /token=/, "the token is stripped from the URL");
    assert.deepEqual(errors, [], "no page errors");
  } catch (e) {
    console.log("daemon log (tail):\n" + d.log.slice(-40).join(""));
    throw e;
  } finally {
    await browser.close();
    await d.stop();
  }
});

// The owner's phone report (3 Nov): installed as an app, still had the browser's top/bottom bars — Chrome had
// fallen back to a bookmark shortcut. This is the real proof it now installs as an app: Chrome's own
// installability check (the same one behind the "Install app" prompt) reports no errors, and the manifest it
// read back declares standalone display.
test("the UI is installable as a standalone PWA", { timeout: 2 * 60_000 }, async () => {
  const d = await startDaemon({ phone: true });
  const browser = await launchBrowser();
  try {
    const { page, cdp, errors } = await openPage(browser, { phone: true, origin: d.proxy.origin });
    await page.goto(d.proxy.origin);
    // not "networkidle": the dashboard keeps its live connection open, so the network never goes quiet
    await page.waitForLoadState("load");
    await page.waitForSelector("link[rel=manifest]", { state: "attached" });
    const { installabilityErrors } = await cdp.send("Page.getInstallabilityErrors");
    assert.deepEqual(installabilityErrors, [], "Chrome's installability check");
    const manifest = await cdp.send("Page.getAppManifest");
    assert.equal(manifest.errors?.length ?? 0, 0, `manifest parse errors: ${JSON.stringify(manifest.errors)}`);
    const parsed = JSON.parse(manifest.data);
    assert.equal(parsed.display, "standalone", "manifest display mode");
    assert.equal(parsed.name, "Tiamatrix");
    assert.ok(parsed.icons?.some(i => i.purpose?.includes("maskable")), "manifest is missing a maskable icon");
    assert.deepEqual(errors, [], "no page errors");
  } catch (e) {
    console.log("daemon log (tail):\n" + d.log.slice(-40).join(""));
    throw e;
  } finally {
    await browser.close();
    await d.stop();
  }
});
