import type { Agent, AgentState, Level } from "./types";

const FAMILY: Record<string, string> = { opus: "Opus", sonnet: "Sonnet", haiku: "Haiku", fable: "Fable" };

/** claude-opus-5-5 → "Opus 5.5", claude-haiku-4-5-20251001 → "Haiku 4.5". Unknown IDs pass through. */
export function modelName(id: string | undefined | null): string {
  if (!id) return "";
  const s = id.trim();
  let m = /^claude-(opus|sonnet|haiku|fable)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?(?:\[1m\])?$/i.exec(s);
  if (m) return `${FAMILY[m[1].toLowerCase()]} ${m[2]}${m[3] ? "." + m[3] : ""}`;
  m = /^claude-(\d+)(?:-(\d{1,2}))?-(opus|sonnet|haiku)(?:-\d{8})?$/i.exec(s);
  if (m) return `${FAMILY[m[3].toLowerCase()]} ${m[1]}${m[2] ? "." + m[2] : ""}`;
  return s;
}

export function agentModelLabel(a: Pick<Agent, "model" | "harness">): string {
  if (a.harness === "shell") return "Shell";
  return modelName(a.model) || "Default model";
}

export function relativeTime(ms: number | undefined | null, now = Date.now()): string {
  if (!ms) return "never";
  const d = Math.max(0, now - ms);
  const s = Math.floor(d / 1000);
  if (s < 10) return "just now";
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const days = Math.floor(h / 24);
  if (days < 7) return `${days}d ago`;
  return new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function absoluteTime(ms: number | undefined | null): string {
  if (!ms) return "";
  return new Date(ms).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export function clockTime(ms: number): string {
  return new Date(ms).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

export function duration(ms: number | undefined | null): string {
  if (!ms || ms < 0) return "";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)}s`;
  const m = Math.floor(s / 60);
  const rs = Math.round(s % 60);
  if (m < 60) return rs ? `${m}m ${rs}s` : `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

export function plural(n: number, one: string, many = one + "s"): string {
  return `${n} ${n === 1 ? one : many}`;
}

/** Replaces the home folder with ~ for display. */
export function displayPath(p: string | undefined | null): string {
  if (!p) return "";
  return p.replace(/^\/Users\/[^/]+(?=\/|$)/, "~").replace(/^\/home\/[^/]+(?=\/|$)/, "~").replace(/^[A-Za-z]:\\Users\\[^\\]+/, "~");
}

/** A path relative to base when it is inside it. */
export function relPath(p: string, base?: string): string {
  if (base && p.startsWith(base.replace(/\/$/, "") + "/")) return p.slice(base.replace(/\/$/, "").length + 1);
  return displayPath(p);
}

export const STATE_LABEL: Record<AgentState, string> = {
  starting: "Starting",
  working: "Working",
  idle: "Idle",
  waiting: "Needs you",
  stopped: "Stopped",
  exited: "Exited",
  error: "Error",
  queued: "Queued",
  paused: "Paused",
};

export const LEVEL_LABEL: Record<Level, string> = { head: "Head", lead: "Lead", worker: "Worker" };

export function isRunning(s: AgentState): boolean {
  return s === "starting" || s === "working" || s === "idle" || s === "waiting" || s === "paused" || s === "queued";
}

export function slugify(s: string): string {
  return s
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
}

export function firstLine(s: string | undefined | null, max = 160): string {
  if (!s) return "";
  const line = s
    .replace(/```[\s\S]*?```/g, " ")
    .split("\n")
    .map((l) => l.trim())
    .find((l) => l.length > 0) ?? "";
  const clean = line.replace(/^#+\s*/, "").replace(/[*_`]/g, "").replace(/[:,;]\s*$/, "");
  return clean.length > max ? clean.slice(0, max - 1) + "…" : clean;
}
