import test from "node:test";
import assert from "node:assert/strict";

test("a pending answer survives remount/reconnect with its exact version and request identity", async () => {
  const data = new Map();
  globalThis.sessionStorage = { getItem: (k) => data.get(k) ?? null, setItem: (k, v) => data.set(k, v) };
  const first = await import("../src/lib/card-answers.ts?first");
  const pending = {
    scope: "https://daemon.example.test",
    card: { id: "card-one", version: 3, kind: "question", question: "Choose?", state: "pending", card: { type: "decision" } },
    request: { decision: "answer", version: 3, answer_id: "same-request", answer: "Keep my full answer\nincluding this line" },
  };
  const key = first.cardAnswerKey(pending.scope, pending.card.id);
  first.saveCardAnswer(key, pending);
  const reconnect = await import("../src/lib/card-answers.ts?reconnect");
  assert.deepEqual(reconnect.useCardAnswers.getState().items[key], pending);
  assert.equal(reconnect.useCardAnswers.getState().items[reconnect.cardAnswerKey("https://other.example.test", pending.card.id)], undefined);
  reconnect.clearCardAnswer(key);
  const acknowledged = await import("../src/lib/card-answers.ts?acknowledged");
  assert.deepEqual(acknowledged.useCardAnswers.getState().items, {});
});

test("storage failure is reported before a pending answer can be submitted", async () => {
  globalThis.sessionStorage = { getItem: () => null, setItem: () => { throw new Error("storage unavailable"); } };
  const mod = await import("../src/lib/card-answers.ts?unavailable");
  assert.throws(() => mod.saveCardAnswer("key", { scope: "test", card: {}, request: {} }), /storage unavailable/);
  assert.deepEqual(mod.useCardAnswers.getState().items, {});
});

test("malformed saved state cannot break the inbox", async () => {
  globalThis.sessionStorage = { getItem: () => 'null', setItem: () => {} };
  const mod = await import("../src/lib/card-answers.ts?corrupt");
  assert.deepEqual(mod.useCardAnswers.getState().items, {});
});

test("typed and late dictated drafts stay visible after closure, change and reload", async () => {
  const data = new Map();
  globalThis.sessionStorage = { getItem: k => data.get(k) ?? null, setItem: (k, v) => data.set(k, v) };
  const mod = await import("../src/lib/card-answers.ts?drafts");
  const scope = "https://one.test";
  const card = { id: "one", version: 1, state: "pending", card: { type: "decision" }, question: "Old question" };
  const key = mod.cardAnswerKey(scope, card.id);
  mod.saveCardDraft(scope, card, "Typed words");
  const dictation = mod.cardDictationDrafts(scope, card);
  // The original component is gone when the app dictation session delivers.
  dictation.set(key, dictation.get(key) + "\nLate dictated words");
  const reload = await import("../src/lib/card-answers.ts?draftReload");
  const drafts = reload.useCardDrafts.getState().items;
  assert.equal(drafts[key].text, "Typed words\nLate dictated words");
  assert.equal(reload.cardsWithSavedAnswers({}, scope, {}, drafts).one.id, "one");
  assert.deepEqual(reload.cardsWithSavedAnswers({}, "https://other.test", {}, drafts), {});
  const changed = { ...card, version: 2, question: "New question" };
  assert.equal(reload.cardsWithSavedAnswers({ one: changed }, scope, {}, drafts).one.question, "New question");
  reload.saveCardDraft(scope, changed, "Edited words");
  assert.equal(reload.useCardDrafts.getState().items[key].card.version, 1);
  reload.saveCardDraft(scope, changed, "Reviewed words", true);
  assert.equal(reload.useCardDrafts.getState().items[key].card.version, 2);
});

test("a hung submission releases the UI without deleting the saved identity", async () => {
  const data = new Map();
  globalThis.sessionStorage = { getItem: k => data.get(k) ?? null, setItem: (k, v) => data.set(k, v) };
  const mod = await import("../src/lib/card-answers.ts?timeout");
  const p = { scope: "one", card: { id: "one", version: 1 }, request: { answer_id: "same", version: 1, answer: "Words" } };
  const key = mod.cardAnswerKey(p.scope, p.card.id);
  mod.saveCardAnswer(key, p);
  await assert.rejects(mod.boundedCardRequest(new Promise(() => {}), 5), /timed out/);
  assert.deepEqual(mod.useCardAnswers.getState().items[key], p);
  assert.equal(await mod.boundedCardRequest(Promise.resolve("delivered"), 5), "delivered");
});
