// Mock WhatsApp for ?mock=…: a linked account with chats of every message type, and a working
// link flow (a QR code that renews, "scanned" after a few seconds; or a pairing code), history
// sync, sending with the delivery ticks and the rate limit, read, settings and Unlink.
// &wa=unlinked starts unlinked (also ?mock=empty; &wa=link-wait: the code is never "scanned"),
// &wa=syncing mid history sync, &wa=nochats linked with no chats, &wa=logged_out / &wa=error
// those states, &wa=off answers 503 (service off), &wa=none 404 (an older daemon), &wa=ask: the
// head asks to send a message (an approval).
// Media is drawn in the browser (canvas PNGs, a WAV tone, a tiny PDF); there is no video.
import qrcode from "qrcode-generator";
import { ApiError } from "../transport";
import type { Approval, DecideBody } from "../types";
import type { WaChat, WaMessage, WaQR, WaSettings, WaStatus, WaUpload } from "../wa-types";

type Emit = (type: string, data: unknown) => void;
type Ask = (agentId: string, p: Partial<Approval>) => Promise<DecideBody>;

const min = 60_000;
const hour = 60 * min;
const ME = "41791234567@s.whatsapp.net";
const PER_CHAT_MS = 2000;
const QR_EVERY_MS = 20_000;

const hex = (n: number) => Array.from({ length: n }, () => "0123456789ABCDEF"[Math.floor(Math.random() * 16)]).join("");
const mid = () => "3EB0" + hex(16);
const b64 = (s: string) => btoa(s);

// ---------------------------------------------------------------------------------------------
// drawn media

function canvas(w: number, h: number, draw: (c: CanvasRenderingContext2D) => void): string {
  const el = document.createElement("canvas");
  el.width = w;
  el.height = h;
  const c = el.getContext("2d");
  if (c) draw(c);
  return el.toDataURL("image/png");
}

function mountains(w: number, h: number, sky: [string, string], tone: string): string {
  return canvas(w, h, (c) => {
    const g = c.createLinearGradient(0, 0, 0, h);
    g.addColorStop(0, sky[0]);
    g.addColorStop(1, sky[1]);
    c.fillStyle = g;
    c.fillRect(0, 0, w, h);
    c.fillStyle = "rgba(255,255,255,0.85)";
    c.beginPath();
    c.arc(w * 0.78, h * 0.24, h * 0.08, 0, Math.PI * 2);
    c.fill();
    const ridge = (y: number, peaks: number[], fill: string) => {
      c.fillStyle = fill;
      c.beginPath();
      c.moveTo(0, h);
      c.lineTo(0, y);
      peaks.forEach((p, i) => c.lineTo(((i + 0.5) / peaks.length) * w, y - p * h));
      c.lineTo(w, y);
      c.lineTo(w, h);
      c.fill();
    };
    ridge(h * 0.72, [0.25, 0.42, 0.18, 0.35], "rgba(60,80,110,0.55)");
    ridge(h * 0.86, [0.2, 0.08, 0.3, 0.12, 0.22], tone);
  });
}

function sticker(): string {
  return canvas(256, 256, (c) => {
    c.fillStyle = "#ffd54a";
    c.strokeStyle = "#fff";
    c.lineWidth = 14;
    c.beginPath();
    c.arc(128, 128, 104, 0, Math.PI * 2);
    c.fill();
    c.stroke();
    c.fillStyle = "#4a3200";
    c.beginPath();
    c.arc(92, 106, 13, 0, Math.PI * 2);
    c.arc(164, 106, 13, 0, Math.PI * 2);
    c.fill();
    c.lineWidth = 11;
    c.strokeStyle = "#4a3200";
    c.beginPath();
    c.arc(128, 136, 52, 0.15 * Math.PI, 0.85 * Math.PI);
    c.stroke();
  });
}

function avatar(letter: string, a: string, b: string): string {
  return canvas(96, 96, (c) => {
    const g = c.createLinearGradient(0, 0, 96, 96);
    g.addColorStop(0, a);
    g.addColorStop(1, b);
    c.fillStyle = g;
    c.fillRect(0, 0, 96, 96);
    c.fillStyle = "rgba(255,255,255,0.92)";
    c.font = "600 42px system-ui, sans-serif";
    c.textAlign = "center";
    c.textBaseline = "middle";
    c.fillText(letter, 48, 52);
  });
}

/** A mono 8 kHz WAV: a warbling tone with pauses, so the player has something to play. */
function wav(seconds: number): string {
  const rate = 8000;
  const n = Math.floor(rate * seconds);
  const buf = new ArrayBuffer(44 + n * 2);
  const v = new DataView(buf);
  const str = (o: number, s: string) => [...s].forEach((ch, i) => v.setUint8(o + i, ch.charCodeAt(0)));
  str(0, "RIFF");
  v.setUint32(4, 36 + n * 2, true);
  str(8, "WAVEfmt ");
  v.setUint32(16, 16, true);
  v.setUint16(20, 1, true);
  v.setUint16(22, 1, true);
  v.setUint32(24, rate, true);
  v.setUint32(28, rate * 2, true);
  v.setUint16(32, 2, true);
  v.setUint16(34, 16, true);
  str(36, "data");
  v.setUint32(40, n * 2, true);
  for (let i = 0; i < n; i++) {
    const t = i / rate;
    const env = Math.max(0, Math.sin(t * Math.PI * 1.6)) ** 0.6;
    const s = Math.sin(2 * Math.PI * (180 + 40 * Math.sin(t * 7)) * t) * env * 0.25;
    v.setInt16(44 + i * 2, s * 32767, true);
  }
  let bin = "";
  const bytes = new Uint8Array(buf);
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

const PDF = b64(
  "%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
    "3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 300 144]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj\n" +
    "4 0 obj<</Length 44>>stream\nBT /F1 18 Tf 40 70 Td (Mock offer) Tj ET\nendstream endobj\n" +
    "5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n",
);

function qrPNG(code: string): string {
  const qr = qrcode(0, "L");
  qr.addData(code, "Byte");
  qr.make();
  const n = qr.getModuleCount();
  const cell = 6;
  const quiet = 4;
  const size = (n + quiet * 2) * cell;
  return canvas(size, size, (c) => {
    c.fillStyle = "#fff";
    c.fillRect(0, 0, size, size);
    c.fillStyle = "#111";
    for (let r = 0; r < n; r++) for (let col = 0; col < n; col++) if (qr.isDark(r, col)) c.fillRect((col + quiet) * cell, (r + quiet) * cell, cell, cell);
  });
}

// ---------------------------------------------------------------------------------------------

interface Stored {
  chat: WaChat;
  msgs: WaMessage[];
}

export class MockWhatsApp {
  private emit: Emit;
  private ask: Ask | undefined;
  private headId: () => string;
  private mode: string;
  private status: WaStatus;
  private chats = new Map<string, Stored>();
  private media = new Map<string, { type: string; name?: string; data: string }>();
  private lastSend = new Map<string, number>();
  private timers: ReturnType<typeof setTimeout>[] = [];
  private qrTimer: ReturnType<typeof setInterval> | null = null;
  /** the chats of an unlinked account, back after linking again */
  private backup: Stored[] = [];

  constructor(opts: { scenario: string; emit: Emit; ask?: Ask; headId?: () => string }) {
    this.emit = opts.emit;
    this.ask = opts.ask;
    this.headId = opts.headId ?? (() => "");
    const p = new URLSearchParams(typeof location === "undefined" ? "" : location.search).get("wa") ?? "";
    this.mode = p || (opts.scenario === "empty" ? "unlinked" : "");
    const settings: WaSettings = {
      read_receipts: false,
      agents_send: "ask",
      allowed_chats: [],
      notify_head_chats: ["120363041234567890@g.us"],
    };
    this.status = { state: "unlinked", history: { syncing: false }, settings };
    if (this.mode === "off" || this.mode === "none") return;
    if (["unlinked", "link-wait", "logged_out"].includes(this.mode)) {
      this.status.state = this.mode === "link-wait" ? "unlinked" : (this.mode as WaStatus["state"]);
      if (this.mode === "logged_out") this.status.error = "logged out from the phone";
      return;
    }
    this.linked(Date.now() - 9 * 24 * hour);
    if (this.mode === "nochats") return;
    this.seed();
    // linked, but WhatsApp can't be reached: the stored chats stay readable
    if (this.mode === "error") Object.assign(this.status, { state: "error", error: "the connection to WhatsApp failed: websocket closed (1006)" });
    if (this.mode === "syncing") {
      // half the chats are here; the rest arrive while the sync runs
      const all = [...this.chats.values()];
      for (const s of all.slice(5)) this.chats.delete(s.chat.jid);
      this.status.history = { syncing: true, progress: 38 };
      this.syncRest(all.slice(5));
    }
    if (opts.scenario !== "still" && opts.scenario !== "empty" && !this.mode) this.timers.push(setTimeout(() => this.incoming(), 7000));
    if (this.mode === "ask") this.timers.push(setTimeout(() => void this.agentAsks(), 1200));
  }

  /** What the owner gate asks for (DESIGN.md "Owner gate"), or null. */
  static gateReason(method: string, parts: string[]): string | null {
    if (parts[0] === "whatsapp" && parts[1] === "unlink" && method === "POST") return "unlink WhatsApp from this machine";
    return null;
  }

  // ------------------------------------------------------------------ state

  private linked(at: number) {
    Object.assign(this.status, { state: "connected", phone: "+41791234567", name: "Sam", linked_at: at, error: undefined });
  }

  private publish() {
    this.emit("whatsapp.status", this.status);
  }

  private stopTimers() {
    for (const t of this.timers) clearTimeout(t);
    this.timers = [];
    if (this.qrTimer) clearInterval(this.qrTimer);
    this.qrTimer = null;
  }

  private chatOrThrow(jid: string): Stored {
    const s = this.chats.get(jid);
    if (!s) throw new ApiError(404, "no such chat");
    return s;
  }

  private add(jid: string, m: Partial<WaMessage> & Pick<WaMessage, "type">, quiet = false): WaMessage {
    const s = this.chatOrThrow(jid);
    const msg: WaMessage = {
      id: mid(),
      chat_jid: jid,
      sender_jid: m.from_me ? ME : jid,
      sender_name: m.from_me ? "Sam" : s.chat.name,
      from_me: false,
      at: Date.now(),
      status: m.from_me ? "read" : "delivered",
      ...m,
    };
    s.msgs.push(msg);
    s.msgs.sort((a, b) => a.at - b.at);
    if (!s.chat.last || s.chat.last.at <= msg.at) s.chat.last = msg;
    if (!quiet) {
      if (!msg.from_me && msg.type !== "system") s.chat.unread += 1;
      this.emit("whatsapp.message", msg);
      this.emit("whatsapp.chat", s.chat);
    }
    return msg;
  }

  private chat(c: Partial<WaChat> & Pick<WaChat, "jid" | "name" | "kind">): Stored {
    const s: Stored = { chat: { unread: 0, pinned: false, muted: false, archived: false, last: null, avatar: null, ...c }, msgs: [] };
    this.chats.set(c.jid, s);
    return s;
  }

  private mediaOf(m: WaMessage, type: string, data: string, name?: string) {
    this.media.set(m.chat_jid + "/" + m.id, { type, data, name });
  }

  // ------------------------------------------------------------------ seed

  private seed() {
    const now = Date.now();
    const day = (d: number, h: number, mm = 0) => {
      const x = new Date(now);
      x.setDate(x.getDate() - d);
      x.setHours(h, mm, 0, 0);
      return Math.min(x.getTime(), now - 60_000);
    };
    const MUM = "41791112233@s.whatsapp.net";
    const LEA = "41786654321@s.whatsapp.net";
    const BO = "41764455667@s.whatsapp.net";
    const CREW = "120363041234567890@g.us";
    const FAMILY = "120363099887766554@g.us";
    const lea = LEA;
    const marco = "41795550101@s.whatsapp.net";
    const jonas = "41795550202@s.whatsapp.net";
    const png = (d: string) => d.slice(d.indexOf(",") + 1);

    // Climbing crew: a group with photos, a sticker, a reply, reactions, a location and a poll.
    this.chat({ jid: CREW, name: "Climbing crew", kind: "group", avatar: avatar("C", "#3f7fbf", "#6cc3a0") });
    const g = (m: Partial<WaMessage> & Pick<WaMessage, "type">) => this.add(CREW, m, true);
    for (let i = 0; i < 64; i++) {
      const who = [[lea, "Lea Müller"], [marco, "Marco Rossi"], [jonas, "Jonas Weber"]][i % 3];
      g({ type: "text", text: ["Anyone up for the gym tonight?", "I'm in after 7", "Bring the long rope", "Can't, sorry", "Next week then!", "👍"][i % 6], sender_jid: who[0], sender_name: who[1], at: day(9 - Math.floor(i / 8), 9 + (i % 8)) });
    }
    g({ type: "system", text: "Marco added Jonas", at: day(2, 8, 2) });
    g({ type: "text", text: "Saturday at the Gastlosen? Forecast says *sunny* and 14°", sender_jid: marco, sender_name: "Marco Rossi", at: day(1, 18, 4) });
    const sat = g({ type: "text", text: "Yes! I can drive, 3 seats free", from_me: true, at: day(1, 18, 9), reactions: [{ emoji: "🙌", by: "Lea Müller" }, { emoji: "🙌", by: "Marco Rossi" }, { emoji: "❤️", by: "Jonas Weber" }] });
    g({ type: "text", text: "Perfect, I'll take one. Leaving from Bern station?", sender_jid: lea, sender_name: "Lea Müller", at: day(1, 18, 12), quoted: { id: sat.id, text: sat.text, sender_name: "You" } });
    g({ type: "location", text: "Gastlosen parking, Jaun · 46.5904, 7.2861 · https://maps.google.com/?q=46.5904,7.2861", sender_jid: marco, sender_name: "Marco Rossi", at: day(1, 18, 20) });
    const photo = g({
      type: "image",
      caption: "Last time up there 🏔️",
      sender_jid: jonas,
      sender_name: "Jonas Weber",
      at: day(0, 8, 40),
      media: { mime: "image/png", size: 182_400, width: 640, height: 420, downloaded: true },
      reactions: [{ emoji: "😍", by: "Lea Müller" }],
    });
    this.mediaOf(photo, "image/png", png(mountains(640, 420, ["#8ec5ff", "#e9f4ff"], "#3b6b4a")));
    g({ type: "unsupported", text: "Poll: Which route first?", sender_jid: lea, sender_name: "Lea Müller", at: day(0, 8, 52) });
    const st = g({ type: "sticker", sender_jid: marco, sender_name: "Marco Rossi", at: day(0, 9, 5), media: { mime: "image/webp", size: 21_000, width: 256, height: 256, downloaded: true } });
    this.mediaOf(st, "image/png", png(sticker()));
    g({ type: "text", text: "Who brings the quickdraws? I have 12", sender_jid: lea, sender_name: "Lea Müller", at: now - 25 * min });
    this.chats.get(CREW)!.chat.unread = 2;

    // Mum: a pinned DM with a voice note and a photo.
    this.chat({ jid: MUM, name: "Mum", kind: "dm", pinned: true, avatar: avatar("M", "#c46b8e", "#f0b27a") });
    const d = (m: Partial<WaMessage> & Pick<WaMessage, "type">) => this.add(MUM, m, true);
    d({ type: "text", text: "Did you get home ok?", at: day(1, 21, 30) });
    d({ type: "text", text: "Yes, all good. Thanks for dinner!", from_me: true, at: day(1, 21, 41) });
    const pic = d({ type: "image", from_me: true, caption: "The cake 🎂", at: day(1, 21, 42), media: { mime: "image/png", size: 96_200, width: 480, height: 480, downloaded: true } });
    this.mediaOf(
      pic,
      "image/png",
      png(
        canvas(480, 480, (c) => {
          c.fillStyle = "#fbe7d3";
          c.fillRect(0, 0, 480, 480);
          c.fillStyle = "#8a4b2a";
          c.fillRect(110, 220, 260, 150);
          c.fillStyle = "#f6f0ea";
          c.fillRect(110, 200, 260, 40);
          c.fillStyle = "#e25b5b";
          for (let i = 0; i < 5; i++) c.fillRect(140 + i * 48, 150, 10, 50);
        }),
      ),
    );
    const voice = d({ type: "voice", at: now - 50 * min, media: { mime: "audio/ogg; codecs=opus", size: 23_800, duration_ms: 14_000, downloaded: false } });
    this.mediaOf(voice, "audio/wav", wav(14));
    d({ type: "text", text: "Are you coming on Sunday? Grandpa asks 😊", at: now - 48 * min });
    this.chats.get(MUM)!.chat.unread = 2;

    // Lea: documents, a contact, a video, an audio file, an edited and a deleted message, a failed send.
    this.chat({ jid: LEA, name: "Lea Müller", kind: "dm" });
    const l = (m: Partial<WaMessage> & Pick<WaMessage, "type">) => this.add(LEA, m, true);
    l({ type: "text", text: "Here's the offer for the flat, see page 2", at: day(3, 10, 2) });
    const doc = l({ type: "document", caption: "", at: day(3, 10, 3), media: { mime: "application/pdf", size: 245_760, name: "Offer-2026-10.pdf", downloaded: false } });
    this.mediaOf(doc, "application/pdf", PDF, "Offer-2026-10.pdf");
    l({ type: "contact", text: "Dr. Anna Keller · +41 44 123 45 67", at: day(3, 10, 5) });
    l({ type: "text", text: "Thanks! I'll read it tonight", from_me: true, at: day(3, 10, 30), edited: true });
    l({ type: "text", from_me: false, deleted: true, at: day(2, 17, 1) });
    l({ type: "video", caption: "The view from the balcony", at: day(2, 17, 3), media: { mime: "video/mp4", size: 6_291_456, duration_ms: 42_000, width: 720, height: 1280, downloaded: false } });
    const song = l({ type: "audio", at: day(2, 17, 9), media: { mime: "audio/mpeg", size: 3_400_000, name: "landlord-voicemail.mp3", duration_ms: 31_000, downloaded: false } });
    this.mediaOf(song, "audio/wav", wav(6));
    l({ type: "text", text: "Can we visit on Thursday at 6? https://flatfox.ch/en/flat/123456", from_me: true, at: now - 3 * hour });
    l({ type: "text", text: "Or Friday morning works too", from_me: true, at: now - 3 * hour + 40_000, status: "failed", error: "WhatsApp didn't accept the message (it may be offline). Try again." });

    // Bo: delivered but not read yet; long text with formatting.
    this.chat({ jid: BO, name: "Bo Chen", kind: "dm", avatar: avatar("B", "#5b6cc9", "#9a7fe0") });
    this.add(BO, { type: "text", text: "Quick one: are we still on for _lunch_ tomorrow? I booked ~12:00~ 12:30 at the Thai place.", at: now - 6 * hour }, true);
    this.add(BO, { type: "text", text: "Yes, see you there", from_me: true, status: "delivered", at: now - 5 * hour }, true);

    // Family: a muted group with many unread.
    this.chat({ jid: FAMILY, name: "Family", kind: "group", muted: true });
    for (let i = 0; i < 12; i++)
      this.add(FAMILY, { type: "text", text: ["Photos from the weekend coming", "😂😂", "So cute", "When is the next one?"][i % 4], sender_jid: "41790000000@s.whatsapp.net", sender_name: ["Aunt Ruth", "Ben", "Grandpa"][i % 3], at: now - (13 - i) * 17 * min }, true);
    this.chats.get(FAMILY)!.chat.unread = 12;

    // A few quieter chats, and one archived.
    const quiet: [string, string, "dm" | "group", string, number][] = [
      ["41443331122@s.whatsapp.net", "Dentist Dr. Keller", "dm", "Reminder: your appointment is on Tue, 3 Nov at 08:30.", 26 * hour],
      ["41799990011@s.whatsapp.net", "Nina", "dm", "Haha yes exactly", 2 * 24 * hour],
      ["120363055500011122@g.us", "Football Tuesday", "group", "Pitch 3 this week", 3 * 24 * hour],
      ["41798887766@s.whatsapp.net", "Tom Becker", "dm", "Sent you the invoice", 5 * 24 * hour],
    ];
    for (const [jid, name, kind, text, ago] of quiet) {
      this.chat({ jid, name, kind });
      this.add(jid, { type: "text", text, at: now - ago, ...(kind === "group" ? { sender_jid: "41795550303@s.whatsapp.net", sender_name: "Chris" } : {}) }, true);
    }
    this.chat({ jid: "41795551234@s.whatsapp.net", name: "Apartment Zurich", kind: "dm", archived: true });
    this.add("41795551234@s.whatsapp.net", { type: "text", text: "The keys are in the mailbox", at: now - 20 * 24 * hour }, true);
  }

  /** History sync: the missing chats arrive in steps while the progress climbs. */
  private syncRest(rest: Stored[]) {
    let p = this.status.history.progress ?? 0;
    const step = () => {
      p = Math.min(100, p + 12);
      for (const s of p >= 100 ? rest.splice(0) : rest.splice(0, 1)) {
        this.chats.set(s.chat.jid, s);
        this.emit("whatsapp.chat", s.chat);
      }
      this.status.history = p >= 100 ? { syncing: false } : { syncing: true, progress: p };
      this.publish();
      if (p < 100) this.timers.push(setTimeout(step, 1500));
    };
    this.timers.push(setTimeout(step, 1500));
  }

  private incoming() {
    const jid = "41791112233@s.whatsapp.net";
    if (!this.chats.has(jid) || this.status.state !== "connected") return;
    this.add(jid, { type: "text", text: "I'll make your favourite soup ❤️" });
  }

  private async agentAsks() {
    const crew = "120363041234567890@g.us";
    if (!this.ask || !this.chats.has(crew)) return;
    const text = "I have 10 quickdraws and a 70 m rope, I'll bring both.";
    const d = await this.ask(this.headId(), {
      tool: "mcp__tmxg__whatsapp_send",
      input: { chat: crew, chat_name: "Climbing crew", text },
      description: "Send a WhatsApp message to Climbing crew",
    });
    if (d.decision === "allow") this.sendNow(crew, { text });
  }

  // ------------------------------------------------------------------ link flow

  private startQR() {
    const fresh = () => {
      const code = `2@${hex(24)},${b64(hex(32))},${b64(hex(32))},${b64(hex(16))}`;
      const q: WaQR = { code, png: qrPNG(code), expires_at: Date.now() + QR_EVERY_MS };
      this.emit("whatsapp.qr", q);
    };
    this.timers.push(setTimeout(fresh, 400));
    this.qrTimer = setInterval(fresh, QR_EVERY_MS);
    // the owner "scans" it
    const scanIn = this.mode === "link-wait" ? 600_000 : 7000;
    this.timers.push(setTimeout(() => this.paired(), scanIn));
  }

  private paired() {
    this.stopTimers();
    this.status.state = "connecting";
    this.publish();
    this.timers.push(
      setTimeout(() => {
        this.linked(Date.now());
        this.status.history = { syncing: true, progress: 0 };
        this.publish();
        if (!this.backup.length && !this.chats.size) this.seed();
        const all = this.backup.length ? this.backup : [...this.chats.values()];
        this.backup = [];
        this.chats.clear();
        this.syncRest(all);
      }, 1400),
    );
  }

  // ------------------------------------------------------------------ sending

  private sendNow(jid: string, b: { text?: string; reply_to?: string; uploads?: WaUpload[] }): WaMessage {
    const s = this.chatOrThrow(jid);
    const quoted = b.reply_to ? s.msgs.find((m) => m.id === b.reply_to) : undefined;
    const q = quoted ? { id: quoted.id, text: quoted.text || quoted.caption || "", sender_name: quoted.from_me ? "You" : quoted.sender_name } : undefined;
    const ups = b.uploads ?? [];
    const out: WaMessage[] = [];
    if (ups.length === 0) out.push(this.add(jid, { type: "text", text: b.text ?? "", from_me: true, status: "pending", quoted: q }));
    ups.forEach((u, i) => {
      const image = u.type.startsWith("image/");
      const audio = u.type.startsWith("audio/");
      const video = u.type.startsWith("video/");
      const size = Math.floor((u.data.length * 3) / 4);
      const m = this.add(jid, {
        type: image ? "image" : audio ? "audio" : video ? "video" : "document",
        from_me: true,
        status: "pending",
        caption: i === 0 ? b.text || undefined : undefined,
        quoted: i === 0 ? q : undefined,
        media: { mime: u.type || "application/octet-stream", size, name: u.name, downloaded: true },
      });
      this.mediaOf(m, u.type || "application/octet-stream", u.data, u.name);
      out.push(m);
    });
    const tick = (status: WaMessage["status"], ms: number) =>
      this.timers.push(
        setTimeout(() => {
          for (const m of out) {
            m.status = status;
            this.emit("whatsapp.message", m);
          }
        }, ms),
      );
    tick("sent", 700);
    tick("delivered", 1600);
    if (s.chat.kind === "dm") tick("read", 4200);
    return out[out.length - 1];
  }

  // ------------------------------------------------------------------ routes

  route(method: string, parts: string[], q: URLSearchParams, body: unknown): unknown {
    if (this.mode === "none") throw new ApiError(404, "Not found.");
    if (this.mode === "off") throw new ApiError(503, "WhatsApp is off on this daemon (whatsapp.enabled is false in config.json).");
    const [, a, b, c, d] = parts; // "whatsapp", …
    const linked = this.status.state === "connected" || this.status.state === "connecting" || (this.status.state === "error" && !!this.status.phone);
    const needLinked = () => {
      if (!linked) throw new ApiError(503, "WhatsApp isn't linked.");
    };

    if (!a && method === "GET") return this.status;

    if (a === "link" && !b && method === "POST") {
      const req = (body ?? {}) as { method?: string; phone?: string };
      if (linked) throw new ApiError(409, "already linked: unlink first");
      const digits = (req.phone ?? "").replace(/[\s()-]/g, "");
      if (req.method === "phone" && !/^\+\d{8,15}$/.test(digits)) throw new ApiError(400, "the phone number needs the country code, like +41 79 123 45 67");
      this.stopTimers();
      this.status = { ...this.status, state: "linking", error: undefined };
      this.publish();
      if (req.method === "phone") {
        const code = `${hex(4)}-${hex(4)}`;
        this.timers.push(setTimeout(() => this.emit("whatsapp.qr", { pairing_code: code }), 200));
        this.timers.push(setTimeout(() => this.paired(), this.mode === "link-wait" ? 600_000 : 9000));
        return { pairing_code: code };
      }
      this.startQR();
      return null;
    }
    if (a === "link" && b === "cancel" && method === "POST") {
      this.stopTimers();
      this.status = { ...this.status, state: "unlinked" };
      this.publish();
      return null;
    }
    if (a === "unlink" && method === "POST") {
      this.stopTimers();
      this.backup = [...this.chats.values()];
      this.chats.clear();
      this.status = { state: "unlinked", history: { syncing: false }, settings: this.status.settings };
      this.publish();
      return null;
    }
    if (a === "settings" && method === "PUT") {
      const next = body as WaSettings;
      this.status.settings = {
        read_receipts: !!next.read_receipts,
        agents_send: next.agents_send === "allowed" ? "allowed" : "ask",
        allowed_chats: [...new Set(next.allowed_chats ?? [])],
        notify_head_chats: [...new Set(next.notify_head_chats ?? [])],
      };
      this.publish();
      return this.status.settings;
    }

    if (a === "chats" && !b && method === "GET") {
      needLinked();
      const term = (q.get("q") ?? "").toLowerCase();
      const list = [...this.chats.values()]
        .filter((s) => !term || s.chat.name.toLowerCase().includes(term) || s.msgs.some((m) => (m.text ?? m.caption ?? "").toLowerCase().includes(term)))
        .map((s) => s.chat)
        .sort((x, y) => (y.last?.at ?? 0) - (x.last?.at ?? 0));
      return list.slice(0, Number(q.get("limit") || 500));
    }
    if (a === "chats" && b && c === "messages" && method === "GET") {
      needLinked();
      const s = this.chatOrThrow(decodeURIComponent(b));
      const limit = Number(q.get("limit") || 50);
      const before = q.get("before");
      const end = before ? s.msgs.findIndex((m) => m.id === before) : s.msgs.length;
      const upto = end < 0 ? s.msgs.length : end;
      return s.msgs.slice(Math.max(0, upto - limit), upto);
    }
    if (a === "chats" && b && c === "messages" && method === "POST") {
      needLinked();
      const jid = decodeURIComponent(b);
      const req = (body ?? {}) as { text?: string; reply_to?: string; uploads?: WaUpload[] };
      if (!req.text?.trim() && !req.uploads?.length) throw new ApiError(400, "nothing to send");
      if (this.status.state !== "connected") throw new ApiError(503, "WhatsApp isn't connected right now");
      const last = this.lastSend.get(jid) ?? 0;
      if (Date.now() - last < PER_CHAT_MS) throw new ApiError(429, "slow down: tmx sends at most one message every 2 seconds in a chat");
      this.lastSend.set(jid, Date.now());
      return this.sendNow(jid, req);
    }
    if (a === "chats" && b && c === "read" && method === "POST") {
      needLinked();
      const s = this.chatOrThrow(decodeURIComponent(b));
      s.chat.unread = 0;
      this.emit("whatsapp.chat", s.chat);
      return null;
    }
    if (a === "media" && b && c && method === "GET") {
      needLinked();
      const jid = decodeURIComponent(b);
      const id = decodeURIComponent(c);
      const s = this.chatOrThrow(jid);
      const m = s.msgs.find((x) => x.id === id);
      const f = this.media.get(jid + "/" + id);
      if (!m || !f) throw new ApiError(404, m?.type === "video" ? "mock data: no video here" : "no such media");
      if (m.media && !m.media.downloaded) {
        m.media = { ...m.media, downloaded: true };
        this.emit("whatsapp.message", m);
      }
      return { type: f.type, size: Math.floor((f.data.length * 3) / 4), name: f.name, data: f.data };
    }
    if (a === "media" && b && c && d === "ticket" && method === "POST") {
      needLinked();
      return { ticket: "mock-" + hex(12), expires_at: Date.now() + 5 * min };
    }
    throw new ApiError(404, "Not found.");
  }
}
