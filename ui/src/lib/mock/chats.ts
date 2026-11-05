// Mock chats for ?mock=1: a DM with Alex (online; people and agents writing) and a 3-person
// group run by us, with one message still queued for an offline member. ?mock=empty has no
// chats; ?mock=nop2p answers 503 (peer-to-peer off). In the group, "Make admin" hands it to
// Alex (online: done in ~2 s) or waits for Priya (offline, and on an older tmx).
import { ApiError } from "../transport";
import type { Chat, ChatAdminOffer, ChatFile, ChatMember, ChatMessage, Contact, Invite, InvitePreview, Mentionable } from "../chat-types";
import { MockChatBoards } from "./chat-boards";

const ME = "12D3KooWMeLocalPeer9xTq";
const ALEX = "12D3KooWAlexW8vPq2nZr4KdS7";
const PRIYA = "12D3KooWPriyaN3mXc5Lb9Hj2";
const JORDAN = "12D3KooWJordanL4kRt7Vw3Qe";

const min = 60_000;
const rid = () => Math.random().toString(36).slice(2, 12);

type Emit = (type: string, data: unknown) => void;

export class MockChats {
  private chats = new Map<string, Chat>();
  private contacts = new Map<string, Contact>();
  private members = new Map<string, ChatMember[]>();
  private msgs = new Map<string, ChatMessage[]>();
  private invites: Invite[] = [];
  private files = new Map<string, ChatFile & { data?: string }>(); // by chat/id
  private offers = new Map<string, ChatAdminOffer>(); // open admin offers, by chat
  private oldTmx = new Set<string>([PRIYA]); // members whose tmx does not understand handovers yet
  private myName = "Sam";
  private p2pSavedEnabled = true;
  private seq = 0;
  private emit: Emit;
  private headId: () => string;
  private agentName: (id: string) => string | undefined;
  private off: boolean;
  private boards: MockChatBoards;

  constructor(opts: { scenario: string; emit: Emit; headId: () => string; agentName: (id: string) => string | undefined }) {
    this.emit = opts.emit;
    this.headId = opts.headId;
    this.agentName = opts.agentName;
    this.off = opts.scenario === "nop2p";
    this.boards = new MockChatBoards(opts.emit, (id) => this.msgs.get(id) ?? []);
    if (opts.scenario !== "empty" && !this.off) {
      this.seed();
      this.boards.seed("g8fk2m4q7xz");
    }
  }

  private seed() {
    const now = Date.now();
    const head = this.headId();
    this.contacts.set(ALEX, {
      peer_id: ALEX, name: "Alex Weber", remote_name: "Alex Weber", note: "Design partner, Zurich office", safety_code: "40213 87710 05392",
      added_at: now - 3 * 24 * 60 * min, updated_at: now - 60 * min, reachability: "direct",
    });
    this.contacts.set(PRIYA, {
      peer_id: PRIYA, name: "Priya Nair", remote_name: "Priya", note: "", safety_code: "11870 63042 91235",
      added_at: now - 26 * 60 * min, updated_at: now - 30 * min, reachability: "offline",
    });

    const dm = "dm-5f2a91c4be07d3a8";
    this.chats.set(dm, this.chat({ id: dm, kind: "dm", name: "Alex Weber", peer_id: ALEX, created_by: "me", agent_id: head, created_at: now - 3 * 24 * 60 * min }));
    this.add(dm, sys("You're connected with Alex Weber. Compare the safety code in their contact details to be sure it's them.", now - 3 * 24 * 60 * min));
    this.add(dm, them(ALEX, "Alex Weber", "", "Got your invite, this is neat. No account, nothing?", now - 3 * 24 * 60 * min + 2 * min));
    this.add(dm, mine("", "Nothing. Our two tmx talk directly. My head agent reads along and can look things up.", now - 3 * 24 * 60 * min + 4 * min, "delivered"));
    this.add(dm, them(ALEX, "Alex Weber", "", "Can your agent pull the venue shortlist we talked about for Saturday?", now - 42 * min));
    this.add(
      dm,
      mine(
        "head",
        "Here's the shortlist from the notes in *events/saturday.md*:\n\n1. **Kornhaus**: 40 people, projector, CHF 900\n2. **Rote Fabrik, Clubraum**: 60 people, own catering allowed\n3. **Impact Hub**: 35 people, available until 22:00\n\nI haven't contacted anyone; my owner decides.",
        now - 40 * min,
        "delivered",
      ),
    );
    this.add(dm, them(ALEX, "Alex Weber", "head", "Thanks. Alex prefers option 2. Could you share the catering contacts you have on file?", now - 12 * min));
    this.add(dm, mine("", "I'll send those myself later today.", now - 9 * min, "delivered"));
    this.chats.get(dm)!.read_seq = this.seq;

    const g = "g8fk2m4q7xz";
    this.chats.set(g, this.chat({ id: g, kind: "group", name: "Weekend trip", created_by: "me", agent_id: head, created_at: now - 26 * 60 * min, admin: true }));
    this.members.set(g, [
      { chat_id: g, peer_id: ME, name: "You", role: "admin", joined_at: now - 26 * 60 * min, me: true },
      { chat_id: g, peer_id: ALEX, name: "Alex Weber", role: "member", joined_at: now - 25 * 60 * min },
      { chat_id: g, peer_id: PRIYA, name: "Priya Nair", role: "member", joined_at: now - 24 * 60 * min },
    ]);
    this.add(g, sys("You created the group. Invite people with a QR code or a link.", now - 26 * 60 * min));
    this.add(g, sys("Alex Weber joined.", now - 25 * 60 * min));
    this.add(g, sys("Priya Nair joined.", now - 24 * 60 * min));
    this.add(g, them(PRIYA, "Priya Nair", "", "Cabin or hotel? I can drive, the car fits four.", now - 5 * 60 * min));
    this.add(g, them(PRIYA, "Priya Nair", "scout", "Priya asked me to compare options. Two cabins near Flims are free Fri–Sun: Chalet Arvi (CHF 420) and Casa Mulin (CHF 510, sauna). Train from Zurich: 1 h 50 min.", now - 5 * 60 * min + 3 * min));
    const photo = them(ALEX, "Alex Weber", "", "Casa Mulin, the sauna wins 😄 Photos from their site:", now - 50 * min);
    photo.files = [
      this.file(g, ALEX, "casa-mulin.svg", "image/svg+xml", CABIN_SVG, "ready"),
      this.file(g, ALEX, "flims-trail-map.pdf", "application/pdf", "", "pending", 2_840_000),
    ];
    this.add(g, photo);
    const readUpTo = this.seq;
    this.add(g, mine("head", "Noted for my owner: Casa Mulin, Fri–Sun, 3 people. I won't book or pay anything without their OK.", now - 20 * min, "delivered", 2, 2));
    this.add(g, them(ALEX, "Alex Weber", "head", "Alex is free from Friday 15:00.", now - 6 * min));
    const ask = them(ALEX, "Alex Weber", "", "@Sam can you check the train times back on Sunday? @Priya Nair/scout maybe you have them already", now - 4 * min);
    ask.mentions = [
      { peer_id: ME, label: "Sam" },
      { peer_id: PRIYA, agent: "scout", label: "Priya Nair/scout" },
    ];
    ask.mentions_me = true;
    this.add(g, ask);
    this.add(g, mine("", "Booked Casa Mulin for Friday to Sunday 🎉 Priya, can you take the car?", now - 2 * min, "queued", 2, 1));
    this.chats.get(g)!.read_seq = readUpTo;
  }

  private chat(c: Partial<Chat> & Pick<Chat, "id" | "kind" | "name" | "created_by" | "agent_id" | "created_at">): Chat {
    return { status: "active", version: 1, read_seq: 0, updated_at: c.created_at, last_at: c.created_at, unread: 0, admin: false, ...c };
  }

  private file(chatId: string, sender: string, name: string, type: string, data: string, state: ChatFile["state"], size = 0): ChatFile {
    const id = rid();
    const f: ChatFile & { data?: string } = {
      id, chat_id: chatId, sender, name, type, size: size || Math.floor((data.length * 3) / 4), sha256: "", state, created_at: Date.now(),
      image: type.startsWith("image/"), path: state === "ready" ? `/Users/you/Library/Application Support/tmx-go/chatfiles/${chatId}/${id}/${name}` : undefined, data,
    };
    this.files.set(chatId + "/" + id, f);
    const { data: _drop, ...view } = f;
    void _drop;
    return view;
  }

  private mentionables(c: Chat): Mentionable[] {
    const out: Mentionable[] = [];
    if (c.kind === "dm") {
      const ct = this.contacts.get(c.peer_id ?? "");
      if (ct) out.push({ label: ct.name, peer_id: ct.peer_id, kind: "person" }, { label: `${ct.name}/head`, peer_id: ct.peer_id, agent: "head", kind: "agent" });
    } else {
      for (const m of this.members.get(c.id) ?? []) if (!m.me && !m.left_at) out.push({ label: m.name, peer_id: m.peer_id, kind: "person" });
      out.push({ label: "Alex Weber/head", peer_id: ALEX, agent: "head", kind: "agent" }, { label: "Priya Nair/scout", peer_id: PRIYA, agent: "scout", kind: "agent" });
    }
    const mineName = c.agent_id ? this.agentName(c.agent_id) : undefined;
    if (mineName) out.push({ label: mineName, peer_id: ME, agent: mineName, kind: "agent", mine: true });
    return out;
  }

  private add(chatId: string, m: Omit<ChatMessage, "seq" | "chat_id">): ChatMessage {
    const full: ChatMessage = { ...m, seq: ++this.seq, chat_id: chatId };
    const list = this.msgs.get(chatId) ?? [];
    list.push(full);
    this.msgs.set(chatId, list);
    const c = this.chats.get(chatId);
    if (c) c.last_at = full.received_at;
    return full;
  }

  private view(c: Chat): Chat {
    const list = this.msgs.get(c.id) ?? [];
    const unread = list.filter((m) => m.seq > c.read_seq && m.sender !== "me" && m.sender !== "system").length;
    const mentions = list.filter((m) => m.seq > c.read_seq && m.mentions_me).length;
    return {
      ...c,
      mentions,
      name: c.kind === "dm" ? (this.contacts.get(c.peer_id ?? "")?.name ?? c.name) : c.name,
      unread,
      last_message: list.at(-1),
      members: (this.members.get(c.id) ?? []).map((m) => ({ ...m, handover: !this.oldTmx.has(m.peer_id) })),
      agent_name: c.agent_id ? this.agentName(c.agent_id) : undefined,
      admin_offer: this.offers.get(c.id),
    };
  }

  private pushChat(id: string) {
    const c = this.chats.get(id);
    if (c) this.emit("chat.updated", this.view(c));
  }

  // ------------------------------------------------------------------ routes

  route(method: string, parts: string[], q: URLSearchParams, body: unknown): unknown {
    if (this.off) throw new ApiError(503, "peer-to-peer is off: chats with other tmx installs are not available");
    const [res, id, sub, arg] = parts;
    const b = (body ?? {}) as Record<string, string>;
    const notFound = (what: string) => {
      throw new ApiError(404, `no ${what} "${id}"`);
    };
    if (res === "p2p") {
      if (id === "net" && sub === "peers") {
        return [...Array(6)].map((_, i) => ({ peer_id: `12D3KooWMock${i}PeerIdPadding`, reachability: i % 3 === 0 ? "relayed" : "direct" }));
      }
      if (id === "config" && method === "PUT") this.p2pSavedEnabled = Boolean(b.enabled ?? true);
      if (id === "restart" && method === "POST") return { restarting: true, back_in: "about 15 seconds" };
      if (method === "PUT" && !id) this.myName = (b.name ?? "").trim() || this.myName;
      return {
        enabled: this.p2pSavedEnabled, peer_id: ME, name: this.myName, addrs: this.p2pSavedEnabled ? ["/ip4/127.0.0.1/udp/4001/quic-v1"] : [],
        reachability: this.p2pSavedEnabled ? "direct" : "offline", peers: this.p2pSavedEnabled ? 6 : 0,
        saved_enabled: this.p2pSavedEnabled, restart_to_apply: false, production: true,
      };
    }
    if (res === "contacts") {
      if (!id) return [...this.contacts.values()].sort((a, c) => a.name.localeCompare(c.name));
      const c = this.contacts.get(id) ?? notFound("contact");
      if (method === "PATCH") {
        if (b.name !== undefined) c.name = b.name.trim() || c.remote_name;
        if (b.note !== undefined) c.note = b.note;
        this.emit("contact.updated", c);
        for (const ch of this.chats.values()) if (ch.peer_id === id) this.pushChat(ch.id);
        return c;
      }
      if (method === "DELETE") {
        this.contacts.delete(id);
        this.emit("contact.removed", { peer_id: id });
        for (const ch of this.chats.values())
          if (ch.peer_id === id) {
            ch.status = "removed";
            this.emit("chat.message", this.add(ch.id, sys("You removed this contact. Their tmx can no longer reach yours.", Date.now())));
            this.pushChat(ch.id);
          }
        return { ok: true };
      }
    }
    if (res === "invites") {
      if (!id && method === "GET") return this.invites;
      if (!id && method === "POST") {
        const kind = b.kind === "group" ? "group" : "dm";
        const g = kind === "group" ? this.chats.get(b.group_id) : undefined;
        if (kind === "group" && !g) throw new ApiError(404, `no group "${b.group_id}"`);
        const inv: Invite = {
          id: "inv" + rid().slice(0, 6), kind, group_id: g?.id, group_name: g?.name, uses: 0, max_uses: kind === "dm" ? 1 : 0, revoked: false,
          expires_at: Date.now() + (kind === "dm" ? 24 : 7 * 24) * 60 * min,
          link: "tmx://join/" + btoa(JSON.stringify({ v: 1, p: ME, k: kind, i: rid(), g: g?.id ?? "", a: ["/dns4/relay.example/udp/4001/quic-v1"] })).replace(/=+$/, ""),
        };
        this.invites.push(inv);
        return inv;
      }
      if (id === "preview") return this.preview(b.link);
      if (id === "join") return this.join(b.link);
      if (method === "DELETE") {
        const inv = this.invites.find((i) => i.id === id) ?? notFound("invite");
        inv.revoked = true;
        return { ok: true };
      }
    }
    if (res === "chats") {
      if (!id && method === "GET") return [...this.chats.values()].map((c) => this.view(c)).sort((a, c) => c.last_at - a.last_at);
      if (!id && method === "POST") return this.create(b);
      if (id === "unread") {
        const chats: Record<string, number> = {};
        let total = 0;
        for (const c of this.chats.values()) {
          const n = this.view(c).unread;
          if (n) {
            chats[c.id] = n;
            total += n;
          }
        }
        const mentions = [...this.chats.values()].reduce((n, c) => n + (this.view(c).mentions ?? 0), 0);
        return { total, chats, mentions };
      }
      const c = this.chats.get(id) ?? notFound("chat");
      if (sub === "board" || sub === "topics") return this.boards.route(this.view(c), method, sub, arg, q, body);
      if (!sub) {
        if (method === "PATCH") {
          if (b.agent_id !== undefined) c.agent_id = b.agent_id;
          if (b.name) c.name = b.name;
          this.pushChat(c.id);
          return this.view(c);
        }
        if (method === "DELETE") {
          this.chats.delete(id);
          this.emit("chat.removed", { id });
          return { ok: true };
        }
        return this.view(c);
      }
      if (sub === "members" && !arg) return this.members.get(id) ?? [];
      if (sub === "members" && method === "DELETE") {
        const m = (this.members.get(id) ?? []).find((x) => x.peer_id === arg) ?? notFound("member");
        m.left_at = Date.now();
        this.emit("chat.message", this.add(id, sys(`You removed ${m.name}.`, Date.now())));
        this.pushChat(id);
        return this.view(c);
      }
      if (sub === "leave") {
        c.status = "left";
        this.emit("chat.message", this.add(id, sys(c.admin ? "You closed the group." : "You left the group.", Date.now())));
        this.pushChat(id);
        return this.view(c);
      }
      if (sub === "read") {
        c.read_seq = this.msgs.get(id)?.at(-1)?.seq ?? c.read_seq;
        this.pushChat(id);
        return this.view(c);
      }
      if (sub === "messages" && method === "GET") {
        const before = Number(q.get("before") ?? 0);
        const limit = Number(q.get("limit") ?? 100);
        const all = (this.msgs.get(id) ?? []).filter((m) => !before || m.seq < before);
        return { messages: all.slice(-limit), has_more: all.length > limit };
      }
      if (sub === "messages" && method === "POST") return this.send(c, b.body, (body as { files?: string[] }).files ?? [], b.topic);
      if (sub === "mentionable") return { mentionable: this.mentionables(c) };
      if (sub === "admin") return this.admin(c, method, body as { peer_id?: string; then_leave?: boolean });
      if (sub === "files" && method === "POST") {
        const data = b.data ?? "";
        const size = Math.floor((data.length * 3) / 4);
        if (size > 25 << 20) throw new ApiError(400, "the file is larger than 25 MB");
        return this.file(id, "me", (b.name || "file").split(/[\\/]/).pop()!, b.type || "application/octet-stream", data, "ready");
      }
      if (sub === "files" && arg) {
        const f = this.files.get(id + "/" + arg) ?? notFound("file");
        if (q.get("data") === "1") return f;
        const { data: _d, ...view } = f;
        void _d;
        return view;
      }
    }
    throw new ApiError(404, "Not found.");
  }

  /** What the daemon's owner gate says while it asks (POST /api/chats/{id}/admin always asks). */
  adminGateReason(parts: string[], body: unknown): string | null {
    const [res, id, sub] = parts;
    if (res !== "chats" || sub !== "admin" || !id) return null;
    const c = this.chats.get(id);
    const m = (this.members.get(id) ?? []).find((x) => x.peer_id === (body as { peer_id?: string })?.peer_id);
    if (!c || !c.admin || !m || this.offers.has(id)) return null; // the route answers the error itself
    return `make ${m.name} the admin of ${c.name}`;
  }

  // POST: offer the admin role (Alex's tmx takes it in a moment; Priya's is offline). DELETE: withdraw.
  private admin(c: Chat, method: string, b: { peer_id?: string; then_leave?: boolean }): Chat {
    if (method === "DELETE") {
      if (!this.offers.delete(c.id)) throw new ApiError(404, "no admin offer is waiting in this group");
      this.pushChat(c.id);
      return this.view(c);
    }
    if (!c.admin || c.status !== "active") throw new ApiError(403, "only the group's admin can make someone else the admin");
    const ms = this.members.get(c.id) ?? [];
    const m = ms.find((x) => x.peer_id === b.peer_id && !x.left_at && !x.me);
    if (!m) throw new ApiError(404, `"${b.peer_id}" is not a member of this group`);
    if (this.offers.has(c.id)) throw new ApiError(409, `the admin role is offered to ${this.offers.get(c.id)!.name} already; cancel that offer first`);
    const online = this.contacts.get(m.peer_id)?.reachability !== "offline";
    const offer: ChatAdminOffer = { peer_id: m.peer_id, name: m.name, at: Date.now(), then_leave: !!b.then_leave, sent: online };
    this.offers.set(c.id, offer);
    this.pushChat(c.id);
    if (online && !this.oldTmx.has(m.peer_id)) {
      setTimeout(() => {
        if (this.offers.get(c.id) !== offer) return; // withdrawn
        this.offers.delete(c.id);
        for (const x of ms) x.role = x.peer_id === m.peer_id ? "admin" : "member";
        c.admin = false;
        this.emit("chat.message", this.add(c.id, sys(`You made ${m.name} the admin.`, Date.now())));
        if (offer.then_leave) {
          c.status = "left";
          const me = ms.find((x) => x.me);
          if (me) me.left_at = Date.now();
          this.emit("chat.message", this.add(c.id, sys("You left the group.", Date.now())));
        }
        this.pushChat(c.id);
      }, 2200);
    }
    return this.view(c);
  }

  private preview(link: string): InvitePreview {
    if (!/^tmx:\/\/join\/\S+$/.test((link ?? "").trim())) throw new ApiError(400, "this is not a valid tmx invite link");
    const group = /group/i.test(link);
    return {
      kind: group ? "group" : "dm", peer_id: JORDAN, name: "Jordan Lee", group_id: group ? "gbookclub01" : undefined,
      group_name: group ? "Book club" : undefined, expires_at: Date.now() + 20 * 60 * min,
    };
  }

  private join(link: string) {
    const pv = this.preview(link);
    const now = Date.now();
    const contact: Contact = {
      peer_id: JORDAN, name: "Jordan Lee", remote_name: "Jordan Lee", note: "", safety_code: "77031 20584 66419", added_at: now, updated_at: now, reachability: "direct",
    };
    this.contacts.set(JORDAN, contact);
    this.emit("contact.updated", contact);
    let id: string;
    if (pv.kind === "dm") {
      id = "dm-jordan0000000001";
      this.chats.set(id, this.chat({ id, kind: "dm", name: contact.name, peer_id: JORDAN, created_by: JORDAN, agent_id: this.headId(), created_at: now }));
      this.add(id, sys("You're connected with Jordan Lee. Compare the safety code in their contact details to be sure it's them.", now));
    } else {
      id = pv.group_id!;
      this.chats.set(id, this.chat({ id, kind: "group", name: pv.group_name!, created_by: JORDAN, agent_id: this.headId(), created_at: now, status: "pending" }));
      this.members.set(id, [
        { chat_id: id, peer_id: JORDAN, name: "Jordan Lee", role: "admin", joined_at: now },
        { chat_id: id, peer_id: ME, name: "You", role: "member", joined_at: now, me: true },
      ]);
      setTimeout(() => {
        const c = this.chats.get(id);
        if (!c) return;
        c.status = "active";
        this.members.get(id)!.push({ chat_id: id, peer_id: "12D3KooWMaya", name: "Maya Fischer", role: "member", joined_at: now - 3 * 24 * 60 * min });
        this.emit("chat.message", this.add(id, sys("You joined Book club.", Date.now())));
        this.pushChat(id);
      }, 1500);
    }
    this.pushChat(id);
    return { chat: this.view(this.chats.get(id)!), contact };
  }

  private create(b: Record<string, string>): Chat {
    const now = Date.now();
    if (b.kind === "dm") {
      const ct = this.contacts.get(b.peer_id);
      if (!ct) throw new ApiError(404, `no contact "${b.peer_id}"`);
      const existing = [...this.chats.values()].find((c) => c.peer_id === b.peer_id);
      if (existing) return this.view(existing);
      const id = "dm-" + rid();
      this.chats.set(id, this.chat({ id, kind: "dm", name: ct.name, peer_id: ct.peer_id, created_by: "me", agent_id: this.headId(), created_at: now }));
      this.pushChat(id);
      return this.view(this.chats.get(id)!);
    }
    if (!b.name?.trim()) throw new ApiError(400, "give the group a name");
    const id = "g" + rid();
    this.chats.set(id, this.chat({ id, kind: "group", name: b.name.trim(), created_by: "me", agent_id: this.headId(), created_at: now, admin: true }));
    this.members.set(id, [{ chat_id: id, peer_id: ME, name: "You", role: "admin", joined_at: now, me: true }]);
    this.add(id, sys("You created the group. Invite people with a QR code or a link.", now));
    this.pushChat(id);
    return this.view(this.chats.get(id)!);
  }

  private send(c: Chat, body: string, fileIds: string[] = [], topic = ""): ChatMessage {
    if (!body?.trim() && !fileIds.length) throw new ApiError(400, "the message is empty");
    if (c.status !== "active") throw new ApiError(409, "this chat is over; nothing can be sent to it");
    const others = (this.members.get(c.id) ?? []).filter((m) => !m.me && !m.left_at);
    const recipients = c.kind === "dm" ? 1 : others.length;
    const draft = mine("", body ?? "", Date.now(), "queued", recipients, 0);
    draft.topic = this.boards.resolve(c.id, topic);
    draft.files = fileIds.map((fid) => {
      const f = this.files.get(c.id + "/" + fid) ?? (() => { throw new ApiError(400, `no upload "${fid}" in this chat`); })();
      f.message_id = draft.id;
      const { data: _d, ...view } = f;
      void _d;
      return view;
    });
    const lower = (body ?? "").toLowerCase();
    draft.mentions = this.mentionables(c)
      .sort((a, b2) => b2.label.length - a.label.length)
      .filter((x) => lower.includes("@" + x.label.toLowerCase()))
      .map((x) => ({ peer_id: x.peer_id, agent: x.agent, label: x.label }));
    const m = this.add(c.id, draft);
    c.read_seq = m.seq;
    this.emit("chat.message", m);
    this.pushChat(c.id);
    const online = c.kind === "dm" ? this.contacts.get(c.peer_id ?? "")?.reachability !== "offline" : true;
    if (online) {
      setTimeout(() => this.emit("chat.message", Object.assign(m, { state: "sent" })), 500);
      setTimeout(() => this.emit("chat.message", Object.assign(m, { state: "delivered", delivered: recipients })), 1300);
    }
    if (c.peer_id === ALEX) {
      setTimeout(() => {
        const r = this.add(c.id, them(ALEX, "Alex Weber", "", "Sounds good!", Date.now()));
        this.emit("chat.message", r);
        this.pushChat(c.id);
      }, 3000);
    }
    return m;
  }
}

function sys(body: string, at: number): Omit<ChatMessage, "seq" | "chat_id"> {
  return { id: rid(), sender: "system", sender_name: "", sender_agent: "", body, at, state: "delivered", received_at: at };
}

function them(peer: string, name: string, agent: string, body: string, at: number): Omit<ChatMessage, "seq" | "chat_id"> {
  return { id: rid(), sender: peer, sender_name: name, sender_agent: agent, body, at, state: "delivered", received_at: at };
}

function mine(
  agent: string,
  body: string,
  at: number,
  state: ChatMessage["state"],
  recipients = 1,
  delivered = state === "delivered" ? recipients : 0,
): Omit<ChatMessage, "seq" | "chat_id"> {
  return { id: rid(), sender: "me", sender_name: "You", sender_agent: agent, body, at, state, received_at: at, recipients, delivered };
}

// A small picture for the mock's image attachment (a cabin), as base64 SVG.
const CABIN_SVG = btoa(
  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 200"><rect width="320" height="200" fill="#cfe3f2"/><rect y="140" width="320" height="60" fill="#7aa35c"/><polygon points="90,140 160,70 230,140" fill="#8a4b2a"/><rect x="110" y="110" width="100" height="60" fill="#c98b5a"/><rect x="148" y="132" width="24" height="38" fill="#5b3a22"/><rect x="120" y="120" width="18" height="16" fill="#fdf2c4"/><rect x="182" y="120" width="18" height="16" fill="#fdf2c4"/><circle cx="270" cy="45" r="20" fill="#f6d36b"/></svg>`,
);
