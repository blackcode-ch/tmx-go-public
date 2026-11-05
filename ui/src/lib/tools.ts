// One-line summaries and icons for tool calls, shared by the conversation and the inbox.
import {
  Bot,
  FilePen,
  FilePlus2,
  FileText,
  FolderSearch,
  Globe,
  ListChecks,
  ListTodo,
  type LucideIcon,
  Mail,
  MessageCircle,
  MessageCircleQuestion,
  Network,
  Plug,
  Rocket,
  Search,
  Send,
  SquareTerminal,
  Square,
  UserRound,
  Wrench,
  FolderPlus,
  Activity,
  Inbox,
  Reply,
  NotebookPen,
} from "lucide-react";
import { relPath } from "./format";

type Input = Record<string, unknown>;

export interface ToolSummary {
  icon: LucideIcon;
  /** short verb/noun shown first, e.g. "Bash", "Read" */
  label: string;
  /** the interesting part (command, path, pattern) */
  detail: string;
  /** render detail in mono */
  mono: boolean;
}

function str(v: unknown): string {
  return typeof v === "string" ? v : v == null ? "" : JSON.stringify(v);
}

export function asInput(input: unknown): Input {
  if (input && typeof input === "object" && !Array.isArray(input)) return input as Input;
  if (typeof input === "string") {
    try {
      const v = JSON.parse(input);
      if (v && typeof v === "object") return v as Input;
    } catch {
      /* not JSON */
    }
  }
  return {};
}

const TMX: Record<string, (i: Input) => { icon: LucideIcon; text: string }> = {
  whoami: () => ({ icon: UserRound, text: "Checked its identity" }),
  fleet: () => ({ icon: Network, text: "Looked at the fleet" }),
  project_create: (i) => ({ icon: FolderPlus, text: `Created project ${str(i.name)}` }),
  agent_launch: (i) => ({ icon: Rocket, text: `Launched ${str(i.level) || "agent"} ${str(i.name)}`.replace(/\s+/g, " ").trim() }),
  agent_stop: (i) => ({ icon: Square, text: `Stopped ${str(i.name ?? i.agent)}` }),
  send: (i) => ({ icon: Send, text: `Sent a message to ${str(i.to) === "owner" ? "you" : str(i.to)}` }),
  report: () => ({ icon: Send, text: "Reported to its parent" }),
  inbox: () => ({ icon: Inbox, text: "Checked its inbox" }),
  reply: () => ({ icon: Reply, text: "Replied to a message" }),
  agent_status: (i) => ({ icon: Activity, text: `Checked on ${str(i.name ?? i.agent)}` }),
  ask_owner: (i) => ({ icon: MessageCircleQuestion, text: `Asked you: ${str(i.question)}` }),
  whatsapp_chats: () => ({ icon: MessageCircle, text: "Looked at the WhatsApp chats" }),
  whatsapp_read: (i) => ({ icon: MessageCircle, text: `Read WhatsApp messages${i.chat_name || i.chat ? ` in ${str(i.chat_name || i.chat)}` : ""}` }),
  whatsapp_send: (i) => ({ icon: MessageCircle, text: `WhatsApp message to ${str(i.chat_name || i.chat || i.jid || i.to) || "a chat"}` }),
};

export function summarizeTool(tool: string | undefined, input: unknown, cwd?: string): ToolSummary {
  const i = asInput(input);
  const name = tool ?? "tool";
  const path = (p: unknown) => relPath(str(p), cwd);
  switch (name) {
    case "Bash":
      return { icon: SquareTerminal, label: "Bash", detail: str(i.command).split("\n")[0], mono: true };
    case "BashOutput":
    case "KillShell":
    case "KillBash":
      return { icon: SquareTerminal, label: name === "BashOutput" ? "Shell output" : "Kill shell", detail: str(i.bash_id ?? i.shell_id), mono: true };
    case "Read":
      return { icon: FileText, label: "Read", detail: path(i.file_path), mono: true };
    case "Write":
      return { icon: FilePlus2, label: "Write", detail: path(i.file_path), mono: true };
    case "Edit":
      return { icon: FilePen, label: "Edit", detail: path(i.file_path), mono: true };
    case "MultiEdit":
      return {
        icon: FilePen,
        label: "Edit",
        detail: `${path(i.file_path)}${Array.isArray(i.edits) ? ` · ${i.edits.length} changes` : ""}`,
        mono: true,
      };
    case "NotebookEdit":
      return { icon: NotebookPen, label: "Notebook", detail: path(i.notebook_path), mono: true };
    case "Grep":
      return {
        icon: Search,
        label: "Grep",
        detail: [str(i.pattern), i.path ? `in ${path(i.path)}` : "", i.glob ? str(i.glob) : ""].filter(Boolean).join("  "),
        mono: true,
      };
    case "Glob":
      return { icon: FolderSearch, label: "Glob", detail: str(i.pattern), mono: true };
    case "WebFetch":
      return { icon: Globe, label: "Fetch", detail: str(i.url), mono: true };
    case "WebSearch":
      return { icon: Globe, label: "Search the web", detail: str(i.query), mono: false };
    case "TodoWrite":
      return {
        icon: ListTodo,
        label: "Todos",
        detail: Array.isArray(i.todos) ? `${i.todos.length} items` : "",
        mono: false,
      };
    case "Task":
    case "Agent":
      return { icon: Bot, label: "Subagent", detail: str(i.description ?? i.prompt).split("\n")[0], mono: false };
    case "ExitPlanMode":
      return { icon: ListChecks, label: "Plan", detail: "Proposed a plan", mono: false };
  }
  const mcp = /^mcp__([^_]+(?:_[^_]+)*?)__(.+)$/.exec(name);
  if (mcp) {
    const [, server, t] = mcp;
    if (server === "tmx" && TMX[t]) {
      const r = TMX[t](i);
      return { icon: r.icon, label: "", detail: r.text, mono: false };
    }
    if (server === "tmx") return { icon: Mail, label: "tmx", detail: t.replace(/_/g, " "), mono: false };
    return { icon: Plug, label: server, detail: t.replace(/_/g, " "), mono: false };
  }
  return { icon: Wrench, label: name, detail: "", mono: false };
}

/** Claude's Read output: "     1\tcode". Returns null when it isn't that shape. */
export function parseNumberedLines(text: string): { n: string; code: string }[] | null {
  const lines = text.replace(/\n$/, "").split("\n");
  if (lines.length < 1) return null;
  const out: { n: string; code: string }[] = [];
  for (const l of lines) {
    const m = /^\s*(\d+)\t(.*)$/.exec(l);
    if (!m) return null;
    out.push({ n: m[1], code: m[2] });
  }
  return out;
}

const EXT_LANG: Record<string, string> = {
  ts: "ts",
  tsx: "tsx",
  js: "js",
  jsx: "jsx",
  mjs: "js",
  cjs: "js",
  go: "go",
  py: "python",
  rs: "rust",
  json: "json",
  md: "markdown",
  css: "css",
  html: "html",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  yml: "yaml",
  yaml: "yaml",
  toml: "toml",
  sql: "sql",
  prisma: "prisma",
  swift: "swift",
  ps1: "powershell",
  rb: "ruby",
  java: "java",
  kt: "kotlin",
  c: "c",
  h: "c",
  cpp: "cpp",
};

export function langFromPath(p: string | undefined): string {
  if (!p) return "text";
  const ext = p.split(".").pop()?.toLowerCase() ?? "";
  return EXT_LANG[ext] ?? "text";
}
