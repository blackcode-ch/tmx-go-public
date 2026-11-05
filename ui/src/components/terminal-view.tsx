import { useEffect, useRef, useState, type ReactNode } from "react";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { FitAddon } from "@xterm/addon-fit";
import { Loader2, PlugZap } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useStore } from "@/lib/store";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { remScale } from "@/lib/text-size";

function themeFor(dark: boolean) {
  return dark
    ? {
        background: "#141414",
        foreground: "#e6e6e6",
        cursor: "#e6e6e6",
        cursorAccent: "#141414",
        selectionBackground: "#3b4a63",
        black: "#1f1f1f",
        brightBlack: "#6b6b6b",
        red: "#f07178",
        green: "#a6d189",
        yellow: "#e5c890",
        blue: "#8caaee",
        magenta: "#ca9ee6",
        cyan: "#81c8be",
        white: "#d4d4d4",
      }
    : {
        background: "#fafafa",
        foreground: "#262626",
        cursor: "#262626",
        cursorAccent: "#fafafa",
        selectionBackground: "#cfdcf5",
        black: "#262626",
        brightBlack: "#8a8a8a",
        red: "#c4384a",
        green: "#3b7d3c",
        yellow: "#9a6b00",
        blue: "#2f5fb3",
        magenta: "#8a3fa8",
        cyan: "#1f7a83",
        white: "#d4d4d4",
      };
}

/**
 * A terminal socket in xterm: an agent's pty (agentTerminalPath) or a harness's sign-in
 * (loginTerminalPath). focusTick: bump it to put the keyboard focus back into the terminal.
 * ended: what shows once the socket closes; without it, Disconnected with a Reconnect button
 * (an agent's pty outlives the socket, so reconnecting attaches again).
 */
export function TerminalView({ path, focusTick, ended, className }: { path: string; focusTick?: number; ended?: ReactNode; className?: string }) {
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const transport = useStore((s) => s.transport);
  const { resolved } = useTheme();
  const [state, setState] = useState<"connecting" | "open" | "closed">("connecting");
  const [gen, setGen] = useState(0);
  /** the process behind the socket ends with it (a sign-in): its cursor goes when it does */
  const finite = ended !== undefined;

  useEffect(() => {
    if (!host.current || !transport) return;
    const t = new Terminal({
      fontFamily: '"Geist Mono Variable", ui-monospace, Menlo, monospace',
      fontSize: 12.5 * remScale(), // grows with the text size (lib/text-size.ts)
      lineHeight: 1.25,
      cursorBlink: true,
      scrollback: 10000,
      allowProposedApi: false,
      theme: themeFor(document.documentElement.classList.contains("dark")),
    });
    const fit = new FitAddon();
    t.loadAddon(fit);
    t.open(host.current);
    term.current = t;
    try {
      fit.fit();
    } catch {
      /* not visible yet */
    }
    setState("connecting");
    const session = transport.openTerminal(path, t.cols, t.rows, {
      onOpen: () => setState("open"),
      onData: (d) => t.write(d),
      onClose: () => {
        if (finite) t.write("\x1b[?25l");
        setState("closed");
      },
    });
    const sub = t.onData((d) => session.write(d));
    const ro = new ResizeObserver(() => {
      try {
        fit.fit();
        session.resize(t.cols, t.rows);
      } catch {
        /* detached */
      }
    });
    ro.observe(host.current);
    t.focus();
    return () => {
      ro.disconnect();
      sub.dispose();
      session.close();
      t.dispose();
      term.current = null;
    };
  }, [path, transport, gen, finite]);

  useEffect(() => {
    if (term.current) term.current.options.theme = themeFor(resolved === "dark");
  }, [resolved]);

  useEffect(() => {
    if (focusTick) term.current?.focus();
  }, [focusTick]);

  return (
    <div className={cn("relative flex min-h-0 flex-1 flex-col p-3 md:p-4", className)}>
      <div className="xterm-host relative min-h-0 flex-1 overflow-hidden rounded-lg border bg-[#fafafa] dark:bg-[#141414]">
        <div ref={host} className="absolute inset-0" />
        {(state === "connecting" || (state === "closed" && ended !== null)) && (
          <div className="absolute inset-x-0 top-0 flex justify-center p-3">
            <div className="flex items-center gap-2 rounded-full border bg-popover px-3 py-1 text-xs text-muted-foreground">
              {state === "connecting" ? (
                <>
                  <Loader2 className="size-3 animate-spin" /> Connecting to the terminal…
                </>
              ) : ended !== undefined ? (
                ended
              ) : (
                <>
                  <PlugZap className="size-3" /> Disconnected
                  <Button size="xs" variant="ghost" className="-mr-1.5 h-5" onClick={() => setGen((g) => g + 1)}>
                    Reconnect
                  </Button>
                </>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
