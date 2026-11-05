// The head on the Matrix (src/lib/matrix-head.ts): the Board folded into the Matrix on 3 Nov. Every
// Board ability keeps working there: the owner's urgency order, archived threads on request, the
// head's open questions and turns per thread, and the Board's old address opening the Matrix.
import test from "node:test";
import assert from "node:assert/strict";
import { cardsByThread, legacyPath, questionsByThread, reorder, withBoard } from "../src/lib/matrix-head.ts";

const th = (id, extra = {}) => ({ id, title: id, kind: "topic", created_at: 1, ...extra });
const card = (id) => ({ thread: th(id), counts: {} });
const board = (threads, extra = {}) => ({ head_id: "head", threads, ordered: true, cards: [], entries: [], ...extra });

test("withBoard: the Matrix's threads follow the owner's urgency order", () => {
  const b = board([th("c"), th("a"), th("general", { kind: "general" })]);
  const got = withBoard([card("general"), card("a"), card("c")], b, false).map((c) => c.thread.id);
  assert.deepEqual(got, ["c", "a", "general"]);
});

test("withBoard: threads the order doesn't know stay after the ordered ones, in their own order", () => {
  const b = board([th("b"), th("a")]);
  const got = withBoard([card("x"), card("a"), card("y"), card("b")], b, false).map((c) => c.thread.id);
  assert.deepEqual(got, ["b", "a", "x", "y"]);
});

test("withBoard: no owner order keeps the Matrix's own order; no board changes nothing", () => {
  const cards = [card("x"), card("a")];
  assert.deepEqual(withBoard(cards, board([th("a"), th("x")], { ordered: false }), false), cards);
  assert.equal(withBoard(cards, null, true), cards);
});

test("withBoard: archived threads appear only when asked for", () => {
  const b = board([th("a"), th("old", { archived: true })]);
  assert.deepEqual(withBoard([card("a")], b, false).map((c) => c.thread.id), ["a"]);
  const shown = withBoard([card("a")], b, true);
  assert.deepEqual(shown.map((c) => c.thread.id), ["a", "old"]);
  assert.equal(shown[1].thread.archived, true);
});

test("questionsByThread: only the head's pending questions, a question with no thread is General's", () => {
  const approvals = {
    q1: { id: "q1", agent_id: "head", state: "pending", thread_id: "a" },
    q2: { id: "q2", agent_id: "head", state: "pending" },
    q3: { id: "q3", agent_id: "head", state: "approved", thread_id: "a" },
    q4: { id: "q4", agent_id: "someone", state: "pending", thread_id: "a" },
  };
  const m = questionsByThread(approvals, "head");
  assert.deepEqual(m.get("a").map((q) => q.id), ["q1"]);
  assert.deepEqual(m.get("general").map((q) => q.id), ["q2"]);
  assert.equal(questionsByThread(approvals, undefined).size, 0);
});

test("cardsByThread: the head's turns land on their thread, oldest first", () => {
  const b = board([], { cards: [{ id: 1, thread_id: "a" }, { id: 5, thread_id: "general" }, { id: 9, thread_id: "a" }] });
  const m = cardsByThread(b);
  assert.deepEqual(m.get("a").map((c) => c.id), [1, 9]);
  assert.deepEqual(m.get("general").map((c) => c.id), [5]);
  assert.equal(cardsByThread(null).size, 0);
});

test("reorder: more urgent and less urgent, and nothing past either end", () => {
  assert.deepEqual(reorder(["a", "b", "c"], "c", -1), ["a", "c", "b"]);
  assert.deepEqual(reorder(["a", "b", "c"], "a", 1), ["b", "a", "c"]);
  assert.equal(reorder(["a", "b", "c"], "a", -1), null);
  assert.equal(reorder(["a", "b", "c"], "c", 1), null);
  assert.equal(reorder(["a", "b"], "gone", 1), null);
});

test("legacyPath: the Board's address opens the Matrix; everything else is left alone", () => {
  assert.equal(legacyPath("/board"), "/matrix");
  assert.equal(legacyPath("/board/"), "/matrix");
  assert.equal(legacyPath("/board?mock=still"), "/matrix?mock=still");
  assert.equal(legacyPath("/matrix"), null);
  assert.equal(legacyPath("/chats/abc/board"), null);
});
