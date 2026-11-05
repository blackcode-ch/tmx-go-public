import { useMemo } from "react";
import { diffLines } from "diff";
import { cn } from "@/lib/utils";

type Line = { kind: "add" | "del" | "ctx"; text: string } | { kind: "fold"; count: number };

function buildLines(oldStr: string, newStr: string, context = 3): Line[] {
  const parts = diffLines(oldStr, newStr);
  const raw: { kind: "add" | "del" | "ctx"; text: string }[] = [];
  for (const p of parts) {
    const lines = p.value.replace(/\n$/, "").split("\n");
    for (const l of lines) raw.push({ kind: p.added ? "add" : p.removed ? "del" : "ctx", text: l });
  }
  // Fold long runs of unchanged lines.
  const out: Line[] = [];
  let i = 0;
  while (i < raw.length) {
    if (raw[i].kind !== "ctx") {
      out.push(raw[i++]);
      continue;
    }
    let j = i;
    while (j < raw.length && raw[j].kind === "ctx") j++;
    const run = raw.slice(i, j);
    const atStart = i === 0;
    const atEnd = j === raw.length;
    const keepHead = atStart ? 0 : context;
    const keepTail = atEnd ? 0 : context;
    if (run.length > keepHead + keepTail + 1) {
      out.push(...run.slice(0, keepHead));
      out.push({ kind: "fold", count: run.length - keepHead - keepTail });
      out.push(...run.slice(run.length - keepTail));
    } else out.push(...run);
    i = j;
  }
  return out;
}

export function diffStats(oldStr: string, newStr: string): { add: number; del: number } {
  let add = 0;
  let del = 0;
  for (const p of diffLines(oldStr, newStr)) {
    if (p.added) add += p.count ?? 0;
    else if (p.removed) del += p.count ?? 0;
  }
  return { add, del };
}

/** Unified diff of an Edit's old_string → new_string. */
export function DiffView({ oldStr, newStr, className, maxHeight = 420 }: { oldStr: string; newStr: string; className?: string; maxHeight?: number }) {
  const lines = useMemo(() => buildLines(oldStr, newStr), [oldStr, newStr]);
  return (
    <div className={cn("scroll-thin overflow-auto rounded-lg border bg-code py-1.5 font-mono text-[0.75rem] leading-[1.6]", className)} style={{ maxHeight }}>
      <table className="w-full border-collapse">
        <tbody>
          {lines.map((l, i) =>
            l.kind === "fold" ? (
              <tr key={i}>
                <td colSpan={2} className="px-3 py-0.5 text-2xs text-subtle-foreground select-none">
                  ⋯ {l.count} unchanged {l.count === 1 ? "line" : "lines"}
                </td>
              </tr>
            ) : (
              <tr
                key={i}
                className={cn(l.kind === "add" && "bg-diff-add-bg", l.kind === "del" && "bg-diff-del-bg")}
              >
                <td
                  className={cn(
                    "w-6 pl-2.5 align-top text-subtle-foreground select-none",
                    l.kind === "add" && "text-diff-add-fg",
                    l.kind === "del" && "text-diff-del-fg",
                  )}
                >
                  {l.kind === "add" ? "+" : l.kind === "del" ? "−" : " "}
                </td>
                <td
                  className={cn(
                    "pr-3 whitespace-pre",
                    l.kind === "ctx" && "text-foreground/75",
                    l.kind === "add" && "text-diff-add-fg",
                    l.kind === "del" && "text-diff-del-fg",
                  )}
                >
                  {l.text || " "}
                </td>
              </tr>
            ),
          )}
        </tbody>
      </table>
    </div>
  );
}
