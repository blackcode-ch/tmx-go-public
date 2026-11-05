import { useEffect } from "react";
import { navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import { ui } from "@/lib/ui";

export function isTyping(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el) return false;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable || !!el.closest?.(".xterm");
}

/** App-wide shortcuts. Page-specific ones (inbox j/k/a/d) live on their pages. */
export function useGlobalHotkeys() {
  useEffect(() => {
    let g = 0;
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (mod && e.key.toLowerCase() === "k") {
        e.preventDefault();
        ui.openPalette(true);
        return;
      }
      if (e.defaultPrevented || isTyping(e.target) || mod || e.altKey) return;
      if (document.querySelector("[role=dialog]")) return;
      const now = Date.now();
      if (g && now - g < 900) {
        g = 0;
        const head = useStore.getState().headId;
        const go: Record<string, string | undefined> = {
          o: paths.overview(),
          i: paths.inbox(),
          b: paths.matrix(), // the Board's old shortcut
          m: paths.matrix(),
          s: paths.settings(),
          h: head ? paths.agent(head) : undefined,
        };
        const p = go[e.key];
        if (p) {
          e.preventDefault();
          navigate(p);
        }
        return;
      }
      switch (e.key) {
        case "g":
          g = now;
          break;
        case "?":
          e.preventDefault();
          ui.openShortcuts(true);
          break;
        case "c":
          e.preventDefault();
          ui.openNewProject(true);
          break;
        case "/":
          e.preventDefault();
          ui.focusComposer();
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}
