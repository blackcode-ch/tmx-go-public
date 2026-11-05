// Pairing a phone end to end with the owner PIN (docs/phone-pairing.md). User story: as the owner, I pair my
// phone by scanning a QR and typing my owner PIN, in one go, so that passkeys never lock me out again.
// The owner sets the PIN once on the host (Settings -> Safety); the phone reaches the daemon through the
// trusted HTTPS proxy, asks to pair, types the PIN and opens the dashboard, also after a reload, at phone
// size. No approval at the host, no passkey, no WebAuthn anywhere.
import { test } from "node:test";
import assert from "node:assert/strict";
import { launchBrowser, openPage, signIn, startDaemon } from "./harness.mjs";

const PIN = "482915";

test("pairing a phone end to end with the owner PIN", { timeout: 5 * 60_000 }, async () => {
  const d = await startDaemon({ phone: true });
  const browser = await launchBrowser();
  try {
    // the owner sets the PIN once, at the machine
    const owner = await openPage(browser, { origin: d.url });
    await signIn(owner.page, d);
    await owner.page.evaluate(() => { location.hash = "/settings"; });
    await owner.page.getByLabel("PIN", { exact: true }).fill(PIN);
    await owner.page.getByLabel("The same PIN again").fill(PIN);
    await owner.page.getByRole("button", { name: "Set PIN" }).click();
    await owner.page.getByText("Owner PIN set").waitFor({ timeout: 20_000 });

    const phone = await openPage(browser, { phone: true, origin: d.proxy.origin });
    await phone.page.addInitScript(() => {
      // fail loudly if anything still asks for a passkey
      const no = () => Promise.reject(new Error("WebAuthn used"));
      if (navigator.credentials) { navigator.credentials.get = no; navigator.credentials.create = no; }
    });
    await phone.page.goto(d.proxy.origin + "/");
    await phone.page.getByRole("heading", { name: "Pair this device" }).waitFor();
    await phone.page.getByLabel("Device name").fill("E2E phone");
    await phone.page.getByRole("button", { name: "Pair this device" }).click();

    // a wrong PIN is refused and the page stays usable
    await phone.page.getByLabel("Owner PIN").fill("000000");
    await phone.page.getByRole("button", { name: "Pair with my PIN" }).click();
    await phone.page.getByRole("alert").waitFor({ timeout: 20_000 });

    await phone.page.getByLabel("Owner PIN").fill(PIN);
    await phone.page.getByRole("button", { name: "Pair with my PIN" }).click();
    await phone.page.getByRole("heading", { name: "Device paired" }).waitFor({ timeout: 30_000 });
    await phone.page.getByRole("button", { name: "Continue" }).click();
    await phone.page.waitForLoadState("networkidle");
    await phone.page.reload();
    await phone.page.waitForLoadState("networkidle");
    const body = await phone.page.locator("body").innerText();
    assert.doesNotMatch(body, /Pair this device|needs your owner token/i, "the paired phone opens the dashboard after a reload");

    const devices = await d.api("GET", "/api/devices");
    assert.ok(devices.find(x => x.name === "E2E phone"), `the owner sees the phone: ${JSON.stringify(devices)}`);
    assert.ok(!d.log.join("").includes("gate/simulated"), "pairing with the PIN never asked the host gate");
    assert.deepEqual(phone.errors, [], "no page errors on the phone");
  } catch (e) {
    console.log("daemon log (tail):\n" + d.log.slice(-60).join(""));
    throw e;
  } finally {
    await browser.close();
    await d.stop();
  }
});
