// Files attached to messages. The daemon stores each upload and appends a note to the message
// that lists the files with their paths (internal/uploads Note); the agent reads them from
// there. This module recognises that note so the conversation shows the files, not the text.
import { api } from "./api";

/** The largest file the daemon takes (internal/uploads MaxFileBytes). */
export const MAX_FILE_BYTES = 20 << 20;
/** How many files one message carries (internal/uploads MaxPerMessage). */
export const MAX_FILES = 10;

export const NOTE_HEADER = "Attached files (open them from these paths):";

export interface Upload {
  id: string;
  agent_id: string;
  name: string;
  type: string;
  size: number;
  path: string;
  image: boolean;
  created_at: number;
  data?: string;
}

export interface AttachedFile {
  /** upload id ("" when the path is not one tmx made) */
  id: string;
  name: string;
  type: string;
  size: string;
  path: string;
  image: boolean;
  /** chat files download separately: "pending" until here, "failed" with error */
  state?: "pending" | "ready" | "failed";
  error?: string;
}

const LINE = /^- (.+) \(([^,()]+), ([^()]+)\): (.+)$/;

/** Splits a message into its own text and the files its note lists. */
export function splitAttachments(text: string): { text: string; files: AttachedFile[] } {
  const at = text.lastIndexOf(NOTE_HEADER);
  if (at < 0 || (at > 0 && !text.slice(0, at).endsWith("\n\n"))) return { text, files: [] };
  const lines = text.slice(at + NOTE_HEADER.length).split("\n").slice(1);
  const files: AttachedFile[] = [];
  for (const line of lines) {
    const m = LINE.exec(line);
    if (!m) return { text, files: [] }; // not our note after all
    const [, name, type, size, path] = m;
    const parts = path.split(/[\\/]/);
    const id = parts.length >= 4 && parts[parts.length - 4] === "uploads" ? parts[parts.length - 2] : "";
    files.push({ id, name, type, size, path, image: type.startsWith("image/") });
  }
  if (files.length === 0) return { text, files: [] };
  return { text: text.slice(0, at).trimEnd(), files };
}

export function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1 << 20)).toFixed(1)} MB`;
}

/** Reads a File as base64 (without the data: prefix). */
export function readBase64(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => {
      const s = String(r.result);
      resolve(s.slice(s.indexOf(",") + 1));
    };
    r.onerror = () => reject(r.error ?? new Error("The file could not be read."));
    r.readAsDataURL(file);
  });
}

// Thumbnails: <img> cannot send the token, so images come as base64 through the API (every
// transport carries that) and are cached as object URLs for the session.
const thumbs = new Map<string, Promise<string>>();

export function uploadImageURL(id: string): Promise<string> {
  let p = thumbs.get(id);
  if (!p) {
    p = api.getUpload(id, true).then((u) => {
      const bin = atob(u.data ?? "");
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      return URL.createObjectURL(new Blob([bytes], { type: u.type }));
    });
    p.catch(() => thumbs.delete(id));
    thumbs.set(id, p);
  }
  return p;
}
