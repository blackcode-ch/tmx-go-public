// The recorder the way the owner uses it (story #3): ONE Record button that records at once (no dialog), screen +
// camera + microphone, no time limit, parts surviving an outage, one recording with its report: the summary on
// top, the video, the key moments (the screen changed), every still. A REAL clock: TMXG_E2E_REC_MINUTES (default 12) =
// three 5-minute segments, the browser offline for TMXG_E2E_OUTAGE_S (default 30) in the middle of the second,
// then Stop. On every PR the job runs it short (1 minute, a 5-s gap: CI budget, tmx-lead 3 Nov); the 12-minute
// run is nightly and on demand.
import { test } from "node:test";
import assert from "node:assert/strict";
import { launchBrowser, openPage, signIn, startDaemon, visibleButtons } from "./harness.mjs";

const MINUTES = Number(process.env.TMXG_E2E_REC_MINUTES || 12);
const SEG_MIN = MINUTES / 12 * 5;          // 12 min -> 5-min segments (config takes whole minutes; see below)
const OUTAGE_AT = MINUTES * 60_000 * 7 / 12, OUTAGE_MS = Number(process.env.TMXG_E2E_OUTAGE_S || 30) * 1000;
const sleep = ms => new Promise(r => setTimeout(r, ms));

// Counts what the page asked the browser for: screen, camera, microphone.
const COUNT_MEDIA = `(() => {
  const md = navigator.mediaDevices; if (!md) return;
  window.__media = { screen: 0, camera: 0, mic: 0 };
  const gum = md.getUserMedia.bind(md), gdm = md.getDisplayMedia.bind(md);
  md.getUserMedia = c => { if (c && c.video) window.__media.camera++; if (c && c.audio) window.__media.mic++; return gum(c); };
  md.getDisplayMedia = c => { window.__media.screen++; return gdm(c); };
})();`;

test(`recorder: one Record button, screen + camera + mic, ${MINUTES} real minutes with a ${OUTAGE_MS / 1000}-s outage, one recording with its summary`, { timeout: (MINUTES + 8) * 60_000 }, async () => {
  const d = await startDaemon({ config: { recorder: { segment_minutes: Math.max(1, Math.round(SEG_MIN)) } } });
  const browser = await launchBrowser();
  try {
    const { context, page, errors } = await openPage(browser, { origin: d.url });
    await context.addInitScript(COUNT_MEDIA);
    await signIn(page, d);
    await page.evaluate(() => { location.hash = "/recordings"; });
    await page.getByRole("button", { name: /^Record$/ }).waitFor();

    // ONE Record button on the page (the owner: "ONE Record button")
    const records = await visibleButtons(page, /^\s*Record\s*$/);
    assert.equal(records.length, 1, `exactly one Record button, got ${JSON.stringify(records)}`);

    // one tap records: no dialog, nothing to choose (the options are the icon beside it)
    const t0 = Date.now();
    await page.getByRole("button", { name: /^Record$/ }).click();
    await page.getByRole("button", { name: /Stop/ }).waitFor({ timeout: 30_000 });
    assert.equal(await page.getByRole("dialog").count(), 0, "no dialog: the tap records");
    const media = await page.evaluate(() => window.__media);
    assert.ok(media.screen >= 1 && media.camera >= 1 && media.mic >= 1, `screen + camera + mic asked for: ${JSON.stringify(media)}`);
    const live = (await d.api("GET", "/api/recordings/active")).recording;
    assert.ok(live && live.id, "a live recording");
    assert.ok(["screen", "window"].includes(live.kind), `a screen capture (the drawn screen reports as a window): ${live.kind}`);
    assert.ok(live.mic, "the microphone is recorded");
    await d.api("PATCH", `/api/recordings/${live.id}`, { title: "E2E review" });

    // the outage: the browser loses the network for 30 s in the middle of the second segment
    await sleep(Math.max(0, OUTAGE_AT - (Date.now() - t0)));
    let failed = 0;
    const onFail = () => failed++;
    page.on("requestfailed", onFail);
    await context.setOffline(true);
    await sleep(OUTAGE_MS);
    await context.setOffline(false);
    page.off("requestfailed", onFail);
    assert.ok(failed > 0, "the outage really cut the page off (requests failed while offline)");
    console.log(`recorder e2e: ${failed} requests failed during the ${OUTAGE_MS / 1000}-s outage`);
    const after = await d.api("GET", `/api/recordings/${live.id}`);
    assert.equal(after.state, "recording", `still recording after the outage: ${after.state} ${after.error || ""}`);

    await sleep(Math.max(0, MINUTES * 60_000 - (Date.now() - t0)));
    await page.getByRole("button", { name: /Stop/ }).click();
    let rec;
    for (let i = 0; i < 240; i++) {     // the window sends what it still has queued, then the daemon finishes
      rec = await d.api("GET", `/api/recordings/${live.id}`);
      if (rec.state !== "recording" && rec.state !== "paused" && !rec.stopping) break;
      await sleep(500);
    }
    const ms = Date.now() - t0;
    console.log(`recorder e2e: ${Math.round(ms / 1000)} s, state ${rec.state}, ${rec.segments.length} segments, ${rec.bytes} bytes, ` +
                `${rec.duration_ms} ms recorded, ${(rec.frames || []).length} frames, error ${JSON.stringify(rec.error || "")}`);
    assert.equal(rec.state, "done", `done, not ${rec.state}: ${rec.error || ""}`);
    assert.ok(!rec.error, `no error: ${rec.error}`);
    assert.equal(rec.segments.length, Math.ceil(MINUTES / Math.max(1, Math.round(SEG_MIN))), "one segment per 5 minutes");
    assert.ok(rec.segments.every(s => s.bytes > 50_000 && s.wav), "every segment has media and the microphone's WAV");
    assert.ok(Math.abs(rec.duration_ms - MINUTES * 60_000) < 20_000, `no time lost: ${rec.duration_ms} ms`);
    assert.ok((rec.frames || []).length >= Math.max(1, MINUTES * 2 - 2), `a still frame every 30 s: ${(rec.frames || []).length}`);
    assert.ok(rec.frames.some(f => f.key), `key moments: the drawn screen changes, so some stills are marked: ${JSON.stringify(rec.frames.map(f => [f.ms, !!f.key]))}`);

    // one recording in the list, with its summary (written by the local model)
    const list = await d.api("GET", "/api/recordings");
    assert.equal(list.length, 1, "one recording");
    let sum;
    for (let i = 0; i < 120; i++) {
      sum = await d.api("GET", `/api/recordings/${live.id}/summary`).catch(() => null);
      if (sum && (sum.state === "done" || sum.state === "failed")) break;
      await sleep(500);
    }
    assert.equal(sum?.state, "done", `summary: ${JSON.stringify(sum)}`);
    assert.match(sum.summary, /E2E/);
    assert.ok(d.model.asks.length >= 1 && d.model.asks[0].images > 0, `the local model read the frames: ${JSON.stringify(d.model.asks)}`);
    await page.evaluate(id => { location.hash = "/recordings/" + id; }, live.id);
    await page.locator('[aria-label="Summary"]').getByText(/E2E: the owner walked through/).waitFor({ timeout: 15_000 });
    // the report's order: the summary on top, then the video, the key moments, the transcript
    const order = await page.evaluate(() => ['[aria-label="Summary"]', "[data-recording-player]", "[data-key-moments]", '[aria-label="Transcript"]']
      .map(q => { const el = document.querySelector(q); return el ? el.getBoundingClientRect().top + window.scrollY : -1; }));
    assert.ok(order.every(y => y >= 0) && order.every((y, i) => i === 0 || y > order[i - 1]), `summary, video, key moments, transcript in that order: ${order}`);
    const moments = page.locator("[data-moment]");
    assert.ok((await moments.count()) >= 2, `the start and at least one change: ${await moments.count()}`);
    await moments.nth(1).click(); // a moment plays the recording from there
    await page.waitForFunction(() => { const v = document.querySelector("[data-recording-player]"); return v && !v.paused && v.currentTime > 0; }, null, { timeout: 15_000 });
    assert.deepEqual(errors, [], "no page errors");
  } catch (e) {
    console.log("daemon log (tail):\n" + d.log.slice(-60).join(""));
    throw e;
  } finally {
    await browser.close();
    await d.stop();
  }
});
