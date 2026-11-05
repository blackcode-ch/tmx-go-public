import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Kbd } from "@/components/ui/kbd";
import { MOD, ui, useUI } from "@/lib/ui";

const GROUPS: { title: string; items: [string, string[]][] }[] = [
  {
    title: "Everywhere",
    items: [
      ["Command palette", [MOD, "K"]],
      ["New project", ["C"]],
      ["Focus the message box", ["/"]],
      ["Keyboard shortcuts", ["?"]],
    ],
  },
  {
    title: "Go to",
    items: [
      ["Home", ["G", "O"]],
      ["Needs you", ["G", "I"]],
      ["Matrix", ["G", "M"]],
      ["The head", ["G", "H"]],
      ["Settings", ["G", "S"]],
    ],
  },
  {
    title: "Conversation",
    items: [
      ["Send", ["Enter"]],
      ["New line", ["Shift", "Enter"]],
      ["Leave the message box", ["Esc"]],
    ],
  },
  {
    title: "Needs you",
    items: [
      ["Next / previous", ["J", "K"]],
      ["Allow", ["A"]],
      ["Deny (with a reason)", ["D"]],
      ["Pick an answer", ["1", "–", "9"]],
    ],
  },
];

export function ShortcutsDialog() {
  const open = useUI((s) => s.shortcuts);
  return (
    <Dialog open={open} onOpenChange={(v) => ui.openShortcuts(v)}>
      <DialogContent className="sm:max-w-[35rem]" onOpenAutoFocus={(e) => e.preventDefault()}>
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>Single-key shortcuts work when you're not typing.</DialogDescription>
        </DialogHeader>
        <div className="grid gap-x-8 gap-y-5 sm:grid-cols-2">
          {GROUPS.map((g) => (
            <section key={g.title}>
              <h3 className="mb-1.5 text-2xs font-medium tracking-wide text-subtle-foreground uppercase">{g.title}</h3>
              <ul className="space-y-0.5">
                {g.items.map(([label, keys]) => (
                  <li key={label} className="flex h-7 items-center justify-between gap-3 text-sm">
                    <span className="text-foreground/90">{label}</span>
                    <span className="flex items-center gap-1">
                      {keys.map((k, i) => (k === "–" ? <span key={i} className="text-subtle-foreground">–</span> : <Kbd key={i}>{k}</Kbd>))}
                    </span>
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}
