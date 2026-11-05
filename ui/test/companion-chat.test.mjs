// The Companion 1:1 conversation store (src/lib/companion-chat.ts): event-driven refresh,
// reply mode and the unread mark, against a fake daemon and a zustand vanilla store.
import test from "node:test";
import assert from "node:assert/strict";
import { createStore } from "zustand/vanilla";
import {
  SEEN_KEY,
  createCompanionChat,
  inFlight,
  initialConvState,
  readSeen,
  refreshesOn,
  unreadCount,
} from "../src/lib/companion-chat.ts";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const BASE = [
  { id: "e1", kind: "message", from: "owner", text: "Friday?", state: "completed", task_id: "t1", at: 1000 },
  { id: "e2", kind: "reply", from: "companion", text: "Two meetings.", task_id: "t1", at: 1100 },
  { id: "e3", kind: "task", from: "agent", agent_name: "worker-1", text: "Read the date", state: "input_required", task_id: "t2", at: 1200 },
  { id: "e4", kind: "question", from: "companion", text: "Which endpoint?", state: "input_required", task_id: "t2", at: 1300 },
];

class HttpError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

function memoryStorage(init = {}) {
  const m = new Map(Object.entries(init));
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), map: m };
}

function setup({ entries = BASE, storage = memoryStorage(), post } = {}) {
  const daemon = { entries: structuredClone(entries), connected: true, busy: false, fetches: 0, posts: [], fail: null };
  const store = createStore(() => initialConvState(readSeen(() => storage)));
  const chat = createCompanionChat({
    store,
    storage: () => storage,
    debounceMs: 15,
    now: () => 5000,
    fetch: async () => {
      daemon.fetches++;
      await sleep(1);
      if (daemon.fail) throw daemon.fail;
      return { connected: daemon.connected, busy: daemon.busy, entries: structuredClone(daemon.entries) };
    },
    post:
      post ??
      (async (text, replyTo) => {
        daemon.posts.push({ text, replyTo });
        return { id: replyTo ?? "t9", state: "queued" };
      }),
  });
  return { daemon, store, chat, storage };
}

test("events: companion.task and Companion's messages refresh (debounced), other events don't", async () => {
  assert.equal(refreshesOn({ type: "companion.task", data: { id: "t1" } }), true);
  assert.equal(refreshesOn({ type: "message.created", data: { from: "companion", to: "head" } }), true);
  assert.equal(refreshesOn({ type: "message.created", data: { from: "worker-1", to: "head" } }), false);
  assert.equal(refreshesOn({ type: "agent.updated", data: {} }), false);

  const { daemon, store, chat } = setup();
  chat.onEvent({ type: "snapshot", data: {} });
  await sleep(10);
  assert.equal(daemon.fetches, 1);
  assert.equal(store.getState().entries.length, 4);
  assert.equal(store.getState().loaded, true);
  assert.equal(store.getState().available, true);

  // a burst of events makes one fetch, after the pause
  daemon.entries.push({ id: "e5", kind: "message", from: "companion", text: "Heads-up", at: 1400 });
  chat.onEvent({ type: "companion.task", data: { id: "t2" } });
  chat.onEvent({ type: "message.created", data: { from: "companion" } });
  chat.onEvent({ type: "companion.task", data: { id: "t2" } });
  chat.onEvent({ type: "message.created", data: { from: "head" } });
  assert.equal(daemon.fetches, 1);
  await sleep(40);
  assert.equal(daemon.fetches, 2);
  assert.deepEqual(
    store.getState().entries.map((e) => e.id),
    ["e1", "e2", "e3", "e4", "e5"],
  );
});

test("an older answer never overwrites a newer one", async () => {
  const { daemon, store, chat } = setup();
  const first = chat.load();
  daemon.entries = daemon.entries.slice(0, 1);
  const second = chat.load();
  await Promise.all([first, second]);
  assert.equal(store.getState().entries.length, 1);
});

test("a daemon without the conversation (404) hides it; other errors keep what was there", async () => {
  const { daemon, store, chat } = setup();
  await chat.load();
  daemon.fail = new HttpError(500, "boom");
  await chat.load();
  assert.equal(store.getState().entries.length, 4);
  assert.equal(store.getState().error, "boom");
  assert.equal(store.getState().available, true);
  daemon.fail = new HttpError(404, "Not found.");
  await chat.load();
  assert.equal(store.getState().available, false);
});

test("reply mode: Answer on a waiting question, the answer goes with reply_to, then the chip goes", async () => {
  const { daemon, store, chat } = setup();
  await chat.load();
  const [, reply, task, question] = store.getState().entries;

  chat.answer(reply); // not a question
  chat.answer(task); // not a question
  assert.equal(store.getState().replyTo, null);

  chat.answer(question);
  assert.deepEqual(store.getState().replyTo, { taskId: "t2", text: "Which endpoint?" });

  chat.clearReply();
  assert.equal(store.getState().replyTo, null);
  chat.answer(question);

  await chat.send("  the staging one  ", store.getState().replyTo.taskId);
  assert.deepEqual(daemon.posts, [{ text: "the staging one", replyTo: "t2" }]);
  const st = store.getState();
  assert.equal(st.replyTo, null);
  assert.equal(st.busy, true);
  assert.equal(st.entries.find((e) => e.id === "e4").state, "answered", "the question shows answered at once");
  const mine = st.entries.at(-1);
  assert.equal(mine.from, "owner");
  assert.equal(mine.text, "the staging one");
  assert.equal(mine.task_id, "t2");
});

test("reply mode: the chip goes when the question is answered elsewhere, stays while it waits", async () => {
  const { daemon, store, chat } = setup();
  await chat.load();
  chat.answer(store.getState().entries[3]);

  daemon.entries.push({ id: "e5", kind: "reply", from: "companion", text: "x", task_id: "t1", at: 1500 });
  await chat.load();
  assert.equal(store.getState().replyTo?.taskId, "t2", "still waiting: the chip stays");

  daemon.entries[3].state = "answered"; // answered in Needs you
  await chat.load();
  assert.equal(store.getState().replyTo, null);
});

test("sending: a failure keeps everything (the composer keeps the text); a 409 refetches", async () => {
  let fail = new HttpError(400, "the message is too long");
  const { daemon, store, chat } = setup({
    post: async () => {
      throw fail;
    },
  });
  await chat.load();
  chat.answer(store.getState().entries[3]);
  await assert.rejects(chat.send("hello", "t2"), /too long/);
  assert.equal(store.getState().entries.length, 4);
  assert.equal(store.getState().replyTo?.taskId, "t2");

  const before = daemon.fetches;
  fail = new HttpError(409, "Companion is not waiting for an answer");
  daemon.entries[3].state = "answered";
  await assert.rejects(chat.send("hello", "t2"), /not waiting/);
  await sleep(10);
  assert.equal(daemon.fetches, before + 1);
  assert.equal(store.getState().replyTo, null, "the refetch shows the question answered and clears the chip");

  await assert.rejects(chat.send("   "), /Write something/);
});

test("a plain message shows at once as the owner's, in flight", async () => {
  const { daemon, store, chat } = setup();
  await chat.load();
  await chat.send("Find the bill");
  assert.deepEqual(daemon.posts, [{ text: "Find the bill", replyTo: undefined }]);
  const mine = store.getState().entries.at(-1);
  assert.equal(mine.id, "local:t9");
  assert.equal(mine.state, "queued");
  assert.equal(inFlight(mine), true);
  await sleep(10); // the refetch after sending replaces it with the daemon's list
  assert.equal(store.getState().entries.length, 4);
});

test("unread: Companion's entries newer than the mark; seeing them moves the mark into storage", async () => {
  const storage = memoryStorage();
  const { daemon, store, chat } = setup({ storage });
  await chat.load();
  assert.equal(unreadCount(store.getState().entries, store.getState().seenAt), 2, "never seen: both of Companion's entries");

  chat.markSeen();
  assert.equal(store.getState().seenAt, 1300);
  assert.equal(storage.getItem(SEEN_KEY), "1300");
  assert.equal(unreadCount(store.getState().entries, store.getState().seenAt), 0);

  // the owner's own message and tmx's notes never count
  daemon.entries.push({ id: "e5", kind: "message", from: "owner", text: "hi", at: 1400 });
  daemon.entries.push({ id: "e6", kind: "error", from: "tmx", text: "nope", at: 1450 });
  await chat.load();
  assert.equal(unreadCount(store.getState().entries, store.getState().seenAt), 0);

  daemon.entries.push({ id: "e7", kind: "reply", from: "companion", text: "done", at: 1500 });
  await chat.load();
  assert.equal(unreadCount(store.getState().entries, store.getState().seenAt), 1);

  // a new page of the same viewer starts from the stored mark
  const again = setup({ storage, entries: daemon.entries });
  await again.chat.load();
  assert.equal(again.store.getState().seenAt, 1300);
  assert.equal(unreadCount(again.store.getState().entries, again.store.getState().seenAt), 1);

  // another window moved the mark
  storage.setItem(SEEN_KEY, "1500");
  again.chat.syncSeen();
  assert.equal(again.store.getState().seenAt, 1500);
});

test("unread: blocked storage doesn't break anything (the mark lasts for the page)", async () => {
  const blocked = {
    getItem() {
      throw new Error("SecurityError");
    },
    setItem() {
      throw new Error("SecurityError");
    },
  };
  const { store, chat } = setup({ storage: blocked });
  assert.equal(store.getState().seenAt, 0);
  await chat.load();
  chat.markSeen();
  assert.equal(store.getState().seenAt, 1300);
  chat.syncSeen();
  assert.equal(store.getState().seenAt, 1300);
});
