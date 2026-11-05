// Settings → Profiles: the agent types, each opening the profile editor.
import { useMemo } from "react";
import { ChevronRight, Eye, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { LevelBadge } from "@/components/badges";
import { Section } from "@/components/page";
import { modelName, plural } from "@/lib/format";
import { profileOf, sortProfiles } from "@/lib/profiles";
import { href, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import type { Profile } from "@/lib/types";

function Tag({ children }: { children: React.ReactNode }) {
  return <span className="inline-flex h-[1.125rem] items-center rounded-[5px] bg-muted px-1.5 text-2xs font-medium text-muted-foreground">{children}</span>;
}

function ProfileRow({ p, users }: { p: Profile; users: number }) {
  const extra = (p.allowed_models ?? []).filter((m) => m !== p.model).length;
  return (
    <a
      href={href(paths.profile(p.slug))}
      className="group flex items-center gap-4 px-4 py-3 outline-none transition-colors duration-150 first:rounded-t-xl last:rounded-b-xl hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
    >
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">
          <span className="truncate text-[0.8125rem] font-semibold">{p.name}</span>
          <span className="font-mono text-2xs text-subtle-foreground">{p.slug}</span>
          {p.built_in && <Tag>Built-in</Tag>}
          {p.modified && <Tag>Edited</Tag>}
          {p.read_only && (
            <Tag>
              <Eye className="mr-1 size-3" aria-hidden />
              Read-only
            </Tag>
          )}
        </div>
        {p.description && <p className="mt-0.5 truncate text-xs text-muted-foreground">{p.description}</p>}
      </div>
      <div className="hidden shrink-0 items-center gap-3 text-xs text-muted-foreground sm:flex">
        <LevelBadge level={p.level} />
        <span className="w-[5.75rem] truncate" title={p.model}>
          {modelName(p.model)}
          {extra > 0 && <span className="text-subtle-foreground"> +{extra}</span>}
        </span>
        <span className="tabular w-[4rem] text-right">{users ? plural(users, "agent") : "unused"}</span>
      </div>
      <ChevronRight className="size-4 shrink-0 text-subtle-foreground transition-transform duration-150 group-hover:translate-x-0.5" />
    </a>
  );
}

export function ProfilesSection() {
  const profiles = useStore((s) => s.profiles);
  const agents = useStore((s) => s.agents);
  const list = useMemo(() => sortProfiles(Object.values(profiles)), [profiles]);
  const users = useMemo(() => {
    const n: Record<string, number> = {};
    for (const a of Object.values(agents)) {
      const p = profileOf(a, profiles);
      if (p) n[p.slug] = (n[p.slug] ?? 0) + 1;
    }
    return n;
  }, [agents, profiles]);

  return (
    <Section
      title="Profiles"
      right={
        <Button variant="outline" size="sm" asChild>
          <a href={href(paths.profile("new"))}>
            <Plus />
            New profile
          </a>
        </Button>
      }
    >
      <p className="-mt-1 mb-3 text-xs text-muted-foreground">
        Agent types: instructions, model, permissions, tool limits and launch rights. The daemon enforces them when an agent starts.
      </p>
      {list.length === 0 ? (
        <div className="rounded-xl border bg-card px-4 py-6 text-center text-sm text-muted-foreground">No profiles yet.</div>
      ) : (
        <div className="divide-y rounded-xl border bg-card">
          {list.map((p) => (
            <ProfileRow key={p.slug} p={p} users={users[p.slug] ?? 0} />
          ))}
        </div>
      )}
    </Section>
  );
}
