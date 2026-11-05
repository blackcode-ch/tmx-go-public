// A tiny hash router: #/agents/abc. Hash routing works the same when the daemon serves the UI
// at / and inside the Wails window, and needs no server-side fallback for deep links.
import { useSyncExternalStore } from "react";

export type Route =
  | { name: "overview" }
  | { name: "inbox" }
  | { name: "matrix" }
  | { name: "settings" }
  | { name: "profile"; slug: string }
  | { name: "agent"; id: string; tab?: string }
  | { name: "project"; id: string }
  | { name: "chats" }
  | { name: "chat"; id: string; view?: string }
  | { name: "recordings"; id?: string }
  | { name: "whatsapp"; jid?: string }
  | { name: "companion" }
  | { name: "usage" }
  | { name: "features" }
  | { name: "notfound"; path: string };

function currentPath(): string {
  const h = window.location.hash.replace(/^#/, "");
  if (!h.startsWith("/")) return "/";
  return h;
}

export function parse(path: string): Route {
  const [p] = path.split("?");
  const parts = p.split("/").filter(Boolean).map(decodeURIComponent);
  if (parts.length === 0) return { name: "overview" };
  if (parts[0] === "inbox" && parts.length === 1) return { name: "inbox" };
  // The Board folded into the Matrix on 3 Nov; its old address opens the Matrix.
  if (parts[0] === "board" && parts.length === 1) return { name: "matrix" };
  if (parts[0] === "matrix" && parts.length === 1) return { name: "matrix" };
  if (parts[0] === "settings" && parts.length === 1) return { name: "settings" };
  if (parts[0] === "settings" && parts[1] === "profiles" && parts[2] && parts.length === 3) return { name: "profile", slug: parts[2] };
  if (parts[0] === "agents" && parts[1]) return { name: "agent", id: parts[1], tab: parts[2] };
  if (parts[0] === "projects" && parts[1]) return { name: "project", id: parts[1] };
  if (parts[0] === "chats" && parts.length === 1) return { name: "chats" };
  if (parts[0] === "chats" && parts[1]) return { name: "chat", id: parts[1], view: parts[2] };
  if (parts[0] === "recordings" && parts.length <= 2) return { name: "recordings", id: parts[1] };
  if (parts[0] === "whatsapp" && parts.length <= 2) return { name: "whatsapp", jid: parts[1] };
  if (parts[0] === "companion" && parts.length === 1) return { name: "companion" };
  if (parts[0] === "usage" && parts.length === 1) return { name: "usage" };
  if (parts[0] === "features" && parts.length === 1) return { name: "features" };
  return { name: "notfound", path: p };
}

const listeners = new Set<() => void>();
window.addEventListener("hashchange", () => listeners.forEach((l) => l()));

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function usePath(): string {
  return useSyncExternalStore(subscribe, currentPath, () => "/");
}

export function useRoute(): Route {
  return parse(usePath());
}

export function navigate(path: string, opts: { replace?: boolean } = {}) {
  const target = "#" + path;
  if (window.location.hash === target) return;
  if (opts.replace) {
    history.replaceState(null, "", target);
    listeners.forEach((l) => l());
  } else {
    window.location.hash = path;
  }
}

export const paths = {
  overview: () => "/",
  inbox: () => "/inbox",
  /** the Matrix: every thread with everything on it (DESIGN.md "Matrix") */
  matrix: () => "/matrix",
  settings: () => "/settings",
  /** "new" opens the editor for a new profile */
  profile: (slug: string) => `/settings/profiles/${encodeURIComponent(slug)}`,
  agent: (id: string, tab?: string) => `/agents/${encodeURIComponent(id)}${tab ? "/" + tab : ""}`,
  project: (id: string) => `/projects/${encodeURIComponent(id)}`,
  chats: () => "/chats",
  chat: (id: string) => `/chats/${encodeURIComponent(id)}`,
  /** a chat's board (DESIGN.md "Chat boards") */
  chatBoard: (id: string) => `/chats/${encodeURIComponent(id)}/board`,
  recordings: () => "/recordings",
  recording: (id: string) => `/recordings/${encodeURIComponent(id)}`,
  /** WhatsApp chats (DESIGN.md "WhatsApp"); a chat by its JID */
  whatsapp: () => "/whatsapp",
  waChat: (jid: string) => `/whatsapp/${encodeURIComponent(jid)}`,
  /** the 1:1 conversation with Companion (DESIGN.md "Companion") */
  companion: () => "/companion",
  /** AI accounts and how much of their quota is used */
  usage: () => "/usage",
  /** every ordered feature with its status and evidence */
  features: () => "/features",
};

export function href(path: string): string {
  return "#" + path;
}
