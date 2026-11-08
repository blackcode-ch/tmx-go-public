// People, Clients, Promises and Reminders (docs/PEOPLE_PROMISES.md): the pure part, so it is
// unit-tested without a browser (test/people-format.test.mjs). No React, no store.

export type Kind = "team" | "external" | "client";

export interface Person {
  key: string;
  name?: string;
  role?: string;
  lang?: string;
  kind: Kind;
  preferred?: string;
  aliases?: string[];
  channels?: { whatsapp?: string; email?: string; github?: string };
  notes?: string;
  updated_at: number;
}

export type PromiseStatus = "open" | "done" | "dropped";

export interface Promise_ {
  id: string;
  person_key: string;
  title: string;
  text?: string;
  owner?: string;
  due?: string;
  status: PromiseStatus;
  created_at: number;
  updated_at: number;
}

export interface Reminder {
  id: string;
  fire_at: number;
  target: string;
  text: string;
  promise_id?: string;
  state: "pending" | "fired" | "cancelled";
  attempts: number;
  last_error?: string;
  fired_at?: number;
}

/** The People page lists team members and external partners; Clients lists clients only. */
export function forPage(all: Person[], page: "people" | "clients"): Person[] {
  const keep = all.filter((p) => (page === "clients" ? p.kind === "client" : p.kind !== "client"));
  return keep.sort((a, b) => displayName(a).localeCompare(displayName(b)));
}

export function displayName(p: Pick<Person, "key" | "name">): string {
  return p.name?.trim() || p.key;
}

/** A key from a name: "Anna Müller" -> "anna-muller" (the API's [a-z0-9][a-z0-9_.-]{0,39}). */
export function keyFromName(name: string): string {
  return name
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9_.-]+/g, "-")
    .replace(/^[^a-z0-9]+/, "")
    .replace(/-+$/, "")
    .slice(0, 40);
}

export function validKey(k: string): boolean {
  return /^[a-z0-9][a-z0-9_.-]{0,39}$/.test(k);
}

/** Local ISO date (YYYY-MM-DD) of a Date. */
export function isoDay(d: Date): string {
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${d.getFullYear()}-${m}-${day}`;
}

export type Bucket = "overdue" | "today" | "soon" | "later" | "nodate" | "closed";

export const BUCKET_LABEL: Record<Bucket, string> = {
  overdue: "Overdue",
  today: "Due today",
  soon: "This week",
  later: "Later",
  nodate: "No date",
  closed: "Closed",
};

const ORDER: Bucket[] = ["overdue", "today", "soon", "later", "nodate", "closed"];

export function bucketOf(p: Pick<Promise_, "status" | "due">, today: string): Bucket {
  if (p.status !== "open") return "closed";
  if (!p.due) return "nodate";
  if (p.due < today) return "overdue";
  if (p.due === today) return "today";
  const week = isoDay(new Date(new Date(today + "T12:00:00").getTime() + 7 * 86400_000));
  return p.due <= week ? "soon" : "later";
}

/** Promises grouped for the page, most urgent first; empty groups left out. */
export function groupPromises(list: Promise_[], today: string): { bucket: Bucket; label: string; items: Promise_[] }[] {
  const by = new Map<Bucket, Promise_[]>();
  for (const p of list) {
    const b = bucketOf(p, today);
    by.set(b, [...(by.get(b) ?? []), p]);
  }
  return ORDER.filter((b) => by.has(b)).map((b) => ({
    bucket: b,
    label: BUCKET_LABEL[b],
    items: by.get(b)!.sort((x, y) => (x.due || "9999").localeCompare(y.due || "9999") || x.created_at - y.created_at),
  }));
}

/** "in 2 h", "in 3 days", "5 min ago" for a reminder time. */
export function whenLabel(at: number, now: number): string {
  const d = at - now;
  const a = Math.abs(d);
  const unit =
    a < 60_000 ? "less than a minute" :
    a < 3_600_000 ? `${Math.round(a / 60_000)} min` :
    a < 86_400_000 ? `${Math.round(a / 3_600_000)} h` :
    `${Math.round(a / 86_400_000)} days`;
  return d >= 0 ? `in ${unit}` : `${unit} ago`;
}

/** Parses the reminder field: "30m", "2h", "3d", or a local "YYYY-MM-DD HH:MM". Unix ms, or null. */
export function parseWhen(raw: string, now: number): number | null {
  const s = raw.trim().toLowerCase();
  const rel = /^(\d+)\s*(m|min|h|d)$/.exec(s);
  if (rel) {
    const n = Number(rel[1]);
    const mult = rel[2] === "d" ? 86_400_000 : rel[2] === "h" ? 3_600_000 : 60_000;
    return n > 0 ? now + n * mult : null;
  }
  const abs = /^(\d{4})-(\d{2})-(\d{2})(?:[ t](\d{2}):(\d{2}))?$/.exec(s);
  if (abs) {
    const [, y, mo, d, h = "09", mi = "00"] = abs;
    const dt = new Date(Number(y), Number(mo) - 1, Number(d), Number(h), Number(mi));
    if (dt.getMonth() !== Number(mo) - 1 || dt.getDate() !== Number(d) || Number(h) > 23 || Number(mi) > 59) return null;
    const t = dt.getTime();
    return t <= now ? null : t;
  }
  return null;
}
