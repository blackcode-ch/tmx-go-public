// Harness display helpers (DESIGN.md "Harnesses"): labels, the fallback list for when
// /api/harnesses has not answered, and reading terminal menus off approvals.
import type { Approval, Harness, HarnessInfo, TerminalMenu } from "./types";

export const HARNESS_LABEL: Record<Harness, string> = {
  claude: "Claude Code",
  codex: "Codex",
  opencode: "opencode",
  agy: "Gemini (Antigravity)",
  antigravity: "Antigravity",
  gemini: "Gemini CLI",
  pi: "pi",
  shell: "Shell",
};

export function harnessLabel(h: string | undefined | null): string {
  return (h && HARNESS_LABEL[h as Harness]) || h || "Claude Code";
}

/** The registry as the daemon ships it, installed state unknown. */
export const FALLBACK_HARNESSES: HarnessInfo[] = [
  { name: "claude", label: "Claude Code", modes: ["chat", "terminal"], installed: true, structured: true },
  { name: "codex", label: "Codex", modes: ["chat", "terminal"], installed: false, structured: true, install: "npm i -g @openai/codex" },
  { name: "opencode", label: "opencode", modes: ["terminal"], installed: false, structured: false, install: "npm i -g opencode-ai" },
  { name: "agy", label: "Gemini (Antigravity)", modes: ["terminal"], installed: false, structured: false, install: "install agy CLI" },
  { name: "gemini", label: "Gemini CLI", modes: ["terminal"], installed: false, structured: false, install: "npm i -g @google/gemini-cli" },
  { name: "pi", label: "pi", modes: ["terminal"], installed: false, structured: false, install: "npm i -g --ignore-scripts @earendil-works/pi-coding-agent" },
  { name: "shell", label: "Shell", modes: ["terminal"], installed: true, structured: false },
];

/** The harness has a chat mode as well as its own screen (Claude Code, Codex): the agent can switch live. */
export function hasChatMode(h: string | undefined | null): boolean {
  return FALLBACK_HARNESSES.some((x) => x.name === (h || "claude") && x.modes.includes("chat"));
}

/** The agent shows its harness's own screen now (a TUI, not a shell). */
export function inOwnScreen(a: { harness: string; mode: string }): boolean {
  return a.mode === "terminal" && a.harness !== "shell";
}

export const TERMINAL_MENU = "terminal_menu";

/** The menu a terminal agent shows on its screen, if this approval is one. */
export function terminalMenuOf(a: Approval): TerminalMenu | null {
  if (a.tool !== TERMINAL_MENU || !a.input || typeof a.input !== "object") return null;
  const m = a.input as Partial<TerminalMenu>;
  return { harness: (m.harness ?? "claude") as Harness, screen: m.screen ?? "dialog", selected: m.selected ?? 0, choices: m.choices ?? null };
}

export function terminalMenuHeadline(m: TerminalMenu): string {
  switch (m.screen) {
    case "approval":
      return "asks for permission on its screen";
    case "trust":
      return "asks whether to trust this folder";
    case "locked":
      return "says the conversation is open elsewhere";
    case "question":
      return "asks you on its screen";
  }
  return "shows a menu on its screen";
}
