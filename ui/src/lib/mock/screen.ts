// Mock of the live mode switch (DESIGN.md "Switching live") for ?mock=1: POST
// /api/agents/{id}/mode switches a Claude or Codex agent between chat and its own screen (mid-turn:
// when the turn ends), and the screen is a small imitation of Claude Code's or Codex's TUI.
import type { TerminalHandlers, TerminalSession } from "../transport";
import { ApiError } from "../transport";
import type { Agent, Mode, ModeChange, TimelineEntry } from "../types";
import { harnessLabel, hasChatMode } from "../harness";
import { isRunning } from "../format";
import type { MockWorld } from "./data";
import { NOT_MINE } from "./resources";

type Patch = (id: string, p: Partial<Agent>) => void;
type Append = (id: string, e: Omit<TimelineEntry, "seq" | "agent_id" | "at">) => void;

export class MockScreen {
  constructor(
    private w: MockWorld,
    private patch: Patch,
    private append: Append,
  ) {}

  route(method: string, parts: string[], body: unknown): unknown {
    const [res, id, action] = parts;
    if (res !== "agents" || action !== "mode" || method !== "POST") return NOT_MINE;
    const a = this.w.agents.get(id);
    if (!a) throw new ApiError(404, `No agent named "${id}".`);
    return this.setMode(a, (body as { mode: Mode }).mode);
  }

  private setMode(a: Agent, to: Mode): ModeChange {
    if (to !== "chat" && to !== "terminal") throw new ApiError(400, "mode must be chat or terminal");
    if (to === "chat" && !hasChatMode(a.harness)) throw new ApiError(400, `${harnessLabel(a.harness)} runs only in terminal mode`);
    const where = to === "chat" ? "in chat" : `in ${harnessLabel(a.harness)}'s own screen`;
    const toWhere = to === "chat" ? "to chat" : `to ${harnessLabel(a.harness)}'s own screen`;
    const out = (note: string, pending = false): ModeChange => ({ agent: structuredClone(a), pending, note });
    if (to === a.mode) {
      if (!a.pending_mode) return out(`${a.name} is ${where} already.`);
      this.patch(a.id, { pending_mode: undefined });
      return out(`${a.name} stays ${where}; the switch is cancelled.`);
    }
    if (!isRunning(a.state)) {
      this.patch(a.id, this.modeFields(a, to));
      return out(`Saved; ${a.name} starts ${where}.`);
    }
    this.patch(a.id, { pending_mode: to });
    if (a.state !== "idle") return out(`${a.name} is in the middle of a turn; it switches ${toWhere} when the turn ends.`, true);
    setTimeout(() => this.run(a.id), 900);
    return out(`Switching ${a.name} ${toWhere}, on the same conversation.`);
  }

  /** Called on every agent change: a pending switch runs once the agent is idle. */
  changed(a: Agent) {
    if (a.pending_mode && a.state === "idle") setTimeout(() => this.run(a.id), 900);
  }

  private modeFields(a: Agent, to: Mode): Partial<Agent> {
    const home = a.home_mode ?? a.mode;
    return { mode: to, home_mode: to === home ? undefined : home, pending_mode: undefined };
  }

  private run(id: string) {
    const a = this.w.agents.get(id);
    const to = a?.pending_mode;
    if (!a || !to || a.state !== "idle") return;
    this.append(id, {
      kind: "system",
      text: to === "terminal" ? `Switched to ${harnessLabel(a.harness)}'s own screen, same conversation` : "Back in chat, same conversation",
    });
    this.patch(id, { ...this.modeFields(a, to), state: "starting", pid: 0 });
    setTimeout(() => this.patch(id, { state: "idle", pid: 40000 + Math.floor(Math.random() * 9000) }), 1100);
  }

  /** A small imitation of the harness's TUI: the conversation so far, an input box, a footer. */
  open(a: Agent, h: TerminalHandlers): TerminalSession {
    const codex = a.harness === "codex";
    const dim = (s: string) => `\x1b[38;5;245m${s}\x1b[0m`;
    const accent = (s: string) => `\x1b[38;5;173m${s}\x1b[0m`;
    let cols = 100;
    let input = "";
    const lines: string[] = codex
      ? [`${accent(">_")} OpenAI Codex ${dim("(mock)")}`, dim(`  model: gpt-6   directory: ${a.cwd}`), ""]
      : [`${accent("✻")} Welcome back to Claude Code ${dim("(mock)")}`, dim(`  ${a.cwd}`), ""];
    const said = (who: string, text: string) => {
      lines.push(codex ? `${dim("›")} ${who}` : `${dim(">")} ${who}`, `${codex ? "•" : accent("⏺")} ${text}`, "");
    };
    said("remember the word kumquat", "Noted: kumquat.");
    const draw = () => {
      const rule = dim("─".repeat(Math.max(20, cols - 2)));
      const shown = lines.slice(-14);
      const footer = codex ? dim("  ⏎ send   ⌃C quit   100% context left") : dim("  ? for shortcuts") + "   " + dim("⏵⏵ auto mode on");
      h.onData(`\x1b[2J\x1b[H${shown.join("\r\n")}\r\n${rule}\r\n${codex ? "›" : ">"} ${input}\r\n${rule}\r\n${footer}`);
      h.onData(`\x1b[${shown.length + 2};${3 + input.length}H`);
    };
    setTimeout(() => {
      h.onOpen();
      draw();
    }, 250);
    return {
      write: (data) => {
        for (const ch of data) {
          if (ch === "\r") {
            const text = input.trim();
            input = "";
            if (text) said(text, /word/i.test(text) ? "The word was kumquat." : "(mock) On it.");
          } else if (ch === "\x7f") input = input.slice(0, -1);
          else if (ch >= " ") input += ch;
        }
        draw();
      },
      resize: (c) => {
        cols = c;
        draw();
      },
      close: () => {},
    };
  }
}
