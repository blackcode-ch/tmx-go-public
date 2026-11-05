// Chat boards in the mock daemon (DESIGN.md "Chat boards"): the "Weekend trip" group has
// topics with messages on them and logs (one line corrected, one topic archived); the DM has
// an empty board. Routes and events as internal/chat/topics.go answers them.
import { ApiError } from "../transport";
import type { Chat, ChatBoard, ChatMessage, ChatTopic, ChatTopicEntry } from "../chat-types";

const ME = "12D3KooWMeLocalPeer9xTq";
const ALEX = "12D3KooWAlexW8vPq2nZr4KdS7";
const PRIYA = "12D3KooWPriyaN3mXc5Lb9Hj2";
const COLORS = ["", "slate", "red", "orange", "amber", "green", "teal", "blue", "violet", "pink"];
const KINDS = ["decision", "action", "info", "link"];
const min = 60_000;

type Emit = (type: string, data: unknown) => void;

const hex = () => Array.from({ length: 16 }, () => Math.floor(Math.random() * 16).toString(16)).join("");
const tid = () => "t" + Math.random().toString(36).slice(2, 12);

export class MockChatBoards {
  private topics = new Map<string, ChatTopic[]>();
  private entries = new Map<string, ChatTopicEntry[]>();
  private order = new Map<string, string[]>();

  constructor(
    private emit: Emit,
    private messages: (chatId: string) => ChatMessage[],
  ) {}

  /** Topics, messages filed under them and logs for the mock group. */
  seed(chatId: string) {
    const now = Date.now();
    const t = (id: string, title: string, color: string, by: string, ago: number, archived = false): ChatTopic => ({
      chat_id: chatId, id, kind: "topic", title, color, archived, created_by: by, created_at: now - ago, updated_at: now - ago, messages: 0,
    });
    this.topics.set(chatId, [
      t("tstay0mulin", "Where we stay", "teal", ALEX, 5 * 60 * min - 10 * min),
      t("ttrain0back", "Getting there", "blue", PRIYA, 4 * 60 * min),
      t("tfood0racl", "Food", "amber", ME, 70 * min),
      t("tdates00old", "Dates", "", PRIYA, 20 * 60 * min, true),
    ]);
    const on = (prefix: string, topic: string) => {
      for (const m of this.messages(chatId)) if (m.body.startsWith(prefix)) m.topic = topic;
    };
    on("Priya asked me to compare", "tstay0mulin");
    on("Casa Mulin, the sauna", "tstay0mulin");
    on("Noted for my owner", "tstay0mulin");
    on("Booked Casa Mulin", "tstay0mulin");
    on("Alex is free from Friday", "ttrain0back");
    on("@Sam can you check the train", "ttrain0back");
    const e = (topic: string, kind: ChatTopicEntry["kind"], text: string, by: string, byName: string, ago: number, extra: Partial<ChatTopicEntry> = {}): ChatTopicEntry => ({
      id: hex(), chat_id: chatId, topic_id: topic, kind, text, by, by_name: byName, at: now - ago, ...extra,
    });
    const drives = e("ttrain0back", "decision", "Priya drives on Friday.", PRIYA, "Priya Nair", 4 * 60 * min);
    this.entries.set(chatId, [
      drives,
      e("tdates00old", "decision", "Fri 14 to Sun 16 November.", PRIYA, "Priya Nair", 19 * 60 * min),
      e("tstay0mulin", "info", "Sauna, self check-in from 16:00.", ALEX, "Alex Weber", 45 * min),
      e("ttrain0back", "decision", "Train both ways: Priya's car is in for repair.", PRIYA, "Priya Nair", 30 * min, { corrects: drives.id }),
      e("tfood0racl", "info", "Raclette on Saturday; Priya brings the cheese.", PRIYA, "Priya Nair", 25 * min, { by_agent: "scout" }),
      e("tstay0mulin", "decision", "Casa Mulin, Friday to Sunday, three people.", "me", "Sam", 15 * min),
      e("ttrain0back", "action", "Sam checks the Sunday trains back.", "me", "Sam", 3 * min),
      e("tstay0mulin", "link", "Booking confirmation", "me", "Sam", 2 * min, { link: "https://casa-mulin.example/booking/4821" }),
    ]);
  }

  /** The topic id a message is written in: an id or a title (a new title opens the topic). */
  resolve(chatId: string, idOrTitle?: string): string {
    const v = (idOrTitle ?? "").trim();
    if (!v || v === "general") return "";
    const list = this.topics.get(chatId) ?? [];
    const t = list.find((x) => x.id === v) ?? list.find((x) => x.title.toLowerCase() === v.toLowerCase());
    if (t) {
      if (t.archived) throw new ApiError(400, `the topic "${t.title}" is archived; restore it to write in it`);
      return t.id;
    }
    return this.create(chatId, v, "").id;
  }

  private create(chatId: string, title: string, color: string): ChatTopic {
    const list = this.topics.get(chatId) ?? [];
    title = title.replace(/\s+/g, " ").trim();
    if (!title) throw new ApiError(400, "the topic needs a title");
    if (title.length > 80) throw new ApiError(400, "the title is longer than 80 characters");
    if (title.toLowerCase() === "general") throw new ApiError(400, "every board has a General topic already");
    if (!COLORS.includes(color)) throw new ApiError(400, `unknown colour "${color}"`);
    if (list.some((x) => !x.archived && x.title.toLowerCase() === title.toLowerCase())) throw new ApiError(409, `a topic "${title}" already exists`);
    const now = Date.now();
    const t: ChatTopic = { chat_id: chatId, id: tid(), kind: "topic", title, color, created_by: ME, created_at: now, updated_at: now, messages: 0 };
    list.push(t);
    this.topics.set(chatId, list);
    this.emit("chat.topic", t);
    return t;
  }

  private find(chatId: string, idOrTitle: string): ChatTopic {
    const v = decodeURIComponent(idOrTitle ?? "");
    if (v === "general" || v.toLowerCase() === "general") return this.general(chatId);
    const list = this.topics.get(chatId) ?? [];
    const t = list.find((x) => x.id === v) ?? list.find((x) => x.title.toLowerCase() === v.toLowerCase());
    if (!t) throw new ApiError(404, `no topic "${v}" on this board`);
    return t;
  }

  private general(chatId: string): ChatTopic {
    return { chat_id: chatId, id: "general", kind: "general", title: "General", created_by: "", created_at: 0, updated_at: 0, messages: 0 };
  }

  board(c: Chat, perTopic = 40): ChatBoard {
    const stored = (this.topics.get(c.id) ?? []).map((x) => ({ ...x, messages: 0, position: undefined as number | undefined }));
    const known = new Set(stored.map((x) => x.id));
    const lane = (m: ChatMessage) => (m.topic && known.has(m.topic) ? m.topic : "general");
    const msgs = this.messages(c.id).filter((m) => m.sender !== "system");
    const gen = { ...this.general(c.id), created_at: c.created_at, position: undefined as number | undefined };
    const all = [gen, ...stored];
    const last = new Map<string, number>();
    for (const m of msgs) {
      const t = all.find((x) => x.id === lane(m))!;
      t.messages++;
      last.set(t.id, Math.max(last.get(t.id) ?? 0, m.received_at));
    }
    const entries = this.entries.get(c.id) ?? [];
    for (const e of entries) last.set(e.topic_id, Math.max(last.get(e.topic_id) ?? 0, e.at));
    const order = this.order.get(c.id) ?? [];
    order.forEach((id, i) => {
      const t = all.find((x) => x.id === id);
      if (t) t.position = i + 1;
    });
    const ordered = all.some((x) => x.position);
    all.sort((a, b) => {
      if (ordered) {
        if (!!a.position !== !!b.position) return a.position ? -1 : 1;
        if (a.position && b.position) return a.position - b.position;
        return a.created_at - b.created_at;
      }
      if ((a.kind === "general") !== (b.kind === "general")) return a.kind === "general" ? -1 : 1;
      return Math.max(b.created_at, last.get(b.id) ?? 0) - Math.max(a.created_at, last.get(a.id) ?? 0);
    });
    const per = new Map<string, number>();
    const latest: ChatMessage[] = [];
    for (let i = msgs.length - 1; i >= 0; i--) {
      const id = lane(msgs[i]);
      const n = (per.get(id) ?? 0) + 1;
      per.set(id, n);
      if (n <= perTopic) latest.unshift(msgs[i]);
    }
    return { chat_id: c.id, topics: all, ordered, entries: [...entries], messages: latest };
  }

  route(c: Chat, method: string, sub: string, arg: string | undefined, q: URLSearchParams, body: unknown): unknown {
    const b = (body ?? {}) as Record<string, unknown>;
    const writable = () => {
      if (c.status !== "active") throw new ApiError(409, "this chat is over; its board can be read, not changed");
    };
    if (sub === "board" && !arg && method === "GET") {
      const per = q.get("per_topic");
      return this.board(c, per === null ? 40 : Number(per));
    }
    if (sub === "topics" && !arg && method === "POST") {
      writable();
      return this.create(c.id, String(b.title ?? ""), String(b.color ?? ""));
    }
    if (sub === "topics" && arg && method === "PATCH") {
      writable();
      const t = this.find(c.id, arg);
      if (t.kind === "general") throw new ApiError(400, "General keeps its name and colour, and is never archived");
      if (typeof b.title === "string") {
        const title = b.title.replace(/\s+/g, " ").trim();
        if (!title) throw new ApiError(400, "the topic needs a title");
        t.title = title;
      }
      if (typeof b.color === "string") t.color = b.color;
      if (typeof b.archived === "boolean") t.archived = b.archived;
      t.updated_at = Date.now();
      this.emit("chat.topic", t);
      return t;
    }
    if (sub === "board" && arg === "log" && method === "POST") {
      writable();
      const t = this.find(c.id, String(b.topic ?? ""));
      const text = String(b.text ?? "").trim();
      const kind = String(b.kind ?? "");
      if (!KINDS.includes(kind)) throw new ApiError(400, "kind must be one of decision, action, info, link");
      if (!text) throw new ApiError(400, "the entry is empty");
      if (text.length > 400) throw new ApiError(400, "keep it to 400 characters (one to three lines)");
      if (/(password|token|secret|api[_ -]?key)\s*[:=]\s*\S+|\bsk-[A-Za-z0-9_-]{16,}/i.test(text))
        throw new ApiError(400, "this looks like it contains a secret (a key, token or password); logs never hold secrets");
      const list = this.entries.get(c.id) ?? [];
      const e: ChatTopicEntry = {
        id: hex(), chat_id: c.id, topic_id: t.id, kind: kind as ChatTopicEntry["kind"], text, link: (b.link as string) || undefined,
        by: "me", by_name: "Sam", at: Date.now(), corrects: (b.corrects as string) || undefined,
      };
      list.push(e);
      this.entries.set(c.id, list);
      this.emit("chat.topic.entry", e);
      return e;
    }
    if (sub === "board" && arg === "order" && method === "PUT") {
      this.order.set(c.id, (b.ids as string[]) ?? []);
      this.emit("chat.board", { chat_id: c.id });
      return { ok: true };
    }
    if (sub === "board" && arg === "move" && method === "POST") {
      writable();
      const t = this.find(c.id, String(b.topic ?? "general"));
      if (t.archived) throw new ApiError(400, `the topic "${t.title}" is archived; restore it first`);
      for (const id of (b.messages as string[]) ?? []) {
        const m = this.messages(c.id).find((x) => x.id === id);
        if (!m) throw new ApiError(404, `no message "${id}" in this chat`);
        m.topic = t.kind === "general" ? "" : t.id;
        this.emit("chat.message", m);
      }
      return { ok: true };
    }
    throw new ApiError(404, "Not found.");
  }
}
