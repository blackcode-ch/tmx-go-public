// Overview: what Companion is doing for tmx (active tasks and the ones that ended in the last day).
import { ArrowRight, Sparkles } from "lucide-react";
import { Button } from "@/components/ui/button";
import { isDone, sortedTasks, useCompanion } from "@/lib/companion";
import { href, paths } from "@/lib/router";
import { useNow } from "@/hooks/use-now";
import { TaskRow } from "./bits";

const DAY = 24 * 3600 * 1000;

export function CompanionStrip() {
  const tasks = useCompanion((s) => s.tasks);
  const now = useNow(60_000);
  const list = sortedTasks(tasks).filter((t) => !isDone(t.state) || now - (t.finished_at || t.updated_at) < DAY);
  if (list.length === 0) return null;
  const active = list.filter((t) => !isDone(t.state)).length;
  return (
    <section className="overflow-hidden rounded-xl border bg-card" aria-label="Companion tasks">
      <div className="flex h-10 items-center gap-2 border-b px-4">
        <Sparkles className="size-3.5 text-brand" />
        <h2 className="text-[0.8125rem] font-medium">Companion</h2>
        <span className="tabular text-xs text-muted-foreground">{active > 0 ? `${active} active` : "nothing running"}</span>
        <Button variant="ghost" size="xs" className="ml-auto text-muted-foreground" asChild>
          <a href={href(paths.settings()) + "?section=companion"}>
            All tasks <ArrowRight />
          </a>
        </Button>
      </div>
      <div className="divide-y">
        {list.slice(0, 4).map((t) => (
          <TaskRow key={t.id} t={t} />
        ))}
      </div>
    </section>
  );
}
