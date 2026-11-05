import { useState } from "react";
import { Archive, ArchiveRestore, Ellipsis, FolderClosed, FolderX, Pause, Play, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { AgentAvatar } from "@/components/agent-avatar";
import { HarnessBadge, ModelBadge, ProjectStatusBadge } from "@/components/badges";
import { CopyButton } from "@/components/copy-button";
import { NewAgentDialog } from "@/components/new-agent-dialog";
import { EmptyState, PageBody, PageHeader, PageTitle, Section } from "@/components/page";
import { nowLine } from "@/components/sidebar";
import { AgentStateLabel } from "@/components/state";
import { Truncate } from "@/components/truncate";
import { useNow } from "@/hooks/use-now";
import { useVisibleAgents } from "@/hooks/use-visible-agents";
import { AgentListFilter } from "@/components/agent-list-filter";
import { api } from "@/lib/api";
import { absoluteTime, displayPath, relativeTime } from "@/lib/format";
import { href, navigate, paths } from "@/lib/router";
import { projectTree, useStore } from "@/lib/store";
import type { Agent, Project, ProjectStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

function AgentLine({ agent, nested }: { agent: Agent; nested?: boolean }) {
  const entries = useStore((s) => s.timelines[agent.id]?.entries);
  const line = nowLine(entries, agent);
  return (
    <a
      href={href(paths.agent(agent.id))}
      className="relative flex min-h-14 items-center gap-3 px-4 py-2.5 outline-none hover:bg-accent/40 focus-visible:bg-accent/50"
    >
      {nested && <span aria-hidden className="w-6 shrink-0" />}
      <AgentAvatar agent={agent} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium">{agent.name}</span>
          {agent.level === "lead" && <span className="text-2xs text-subtle-foreground">lead</span>}
          {agent.harness !== "shell" && (agent.harness !== "claude" || agent.mode === "terminal") && <HarnessBadge harness={agent.harness} mode={agent.mode} />}
          <ModelBadge agent={agent} />
        </div>
        <Truncate className="mt-0.5 text-xs text-muted-foreground">{agent.error || line || agent.role || "No activity yet"}</Truncate>
      </div>
      <div className="hidden w-40 shrink-0 sm:block">
        <AgentStateLabel agent={agent} />
      </div>
      <span className="tabular w-16 shrink-0 text-right text-2xs text-subtle-foreground">{relativeTime(agent.last_activity_at)}</span>
    </a>
  );
}

function DeleteProjectDialog({ project, open, onOpenChange }: { project: Project; open: boolean; onOpenChange: (v: boolean) => void }) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const ok = typed.trim() === project.name;
  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!busy) {
          onOpenChange(v);
          setTyped("");
        }
      }}
    >
      <DialogContent className="sm:max-w-[28.75rem]">
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            if (!ok) return;
            setBusy(true);
            try {
              await api.deleteProject(project.id);
              toast(`Deleted ${project.name}`, { description: "The folder was kept." });
              onOpenChange(false);
              navigate(paths.overview());
            } catch (err) {
              toast.error((err as Error).message);
              setBusy(false);
            }
          }}
        >
          <DialogHeader>
            <DialogTitle>Delete {project.name}?</DialogTitle>
            <DialogDescription>Its lead and workers are stopped and removed, with their conversations.</DialogDescription>
          </DialogHeader>
          <div className="flex items-start gap-2.5 rounded-lg border bg-muted/40 px-3 py-2.5 text-xs">
            <FolderClosed className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
            <p className="text-muted-foreground">
              The folder <span className="font-mono text-foreground">{displayPath(project.folder)}</span> is kept on disk, with
              everything in it.
            </p>
          </div>
          <div className="grid gap-1.5">
            <label htmlFor="del-confirm" className="text-xs text-muted-foreground">
              Type <span className="font-mono font-medium text-foreground">{project.name}</span> to confirm
            </label>
            <Input id="del-confirm" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} autoFocus className="font-mono" />
          </div>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" variant="destructive" disabled={!ok || busy}>
              Delete project
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

async function setStatus(p: Project, status: ProjectStatus) {
  try {
    await api.patchProject(p.id, { status });
    toast({ active: `Resumed ${p.name}`, paused: `Paused ${p.name}`, archived: `Archived ${p.name}` }[status]);
  } catch (e) {
    toast.error((e as Error).message);
  }
}

export function ProjectPage({ id }: { id: string }) {
  const project = useStore((s) => s.projects[id]);
  const agents = useVisibleAgents();
  const [del, setDel] = useState(false);
  const [newAgent, setNewAgent] = useState(false);
  useNow(30_000);

  if (!project) {
    return (
      <div className="flex flex-1 items-center justify-center p-6">
        <EmptyState icon={FolderX} title="This project isn't here" className="w-full max-w-md border-none bg-transparent">
          It may have been deleted.
          <div className="mt-4">
            <Button variant="outline" size="sm" asChild>
              <a href={href(paths.overview())}>Back to overview</a>
            </Button>
          </div>
        </EmptyState>
      </div>
    );
  }

  const { lead, workers } = projectTree(agents, project);
  const all = lead ? [lead, ...workers] : workers;
  const turns = all.reduce((s, a) => s + (a.turns || 0), 0);
  const active = all.filter((a) => a.state === "working" || a.state === "waiting" || a.state === "starting").length;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => setNewAgent(true)} disabled={project.status === "archived"}>
              <Plus />
              New agent
            </Button>
            {project.status === "active" ? (
              <Button variant="outline" size="sm" onClick={() => void setStatus(project, "paused")}>
                <Pause className="fill-current" />
                Pause
              </Button>
            ) : (
              <Button variant="outline" size="sm" onClick={() => void setStatus(project, "active")}>
                <Play className="fill-current" />
                Resume
              </Button>
            )}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon-sm" aria-label="Project actions" className="text-muted-foreground">
                  <Ellipsis className="size-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-48">
                {project.status === "archived" ? (
                  <DropdownMenuItem onSelect={() => void setStatus(project, "active")}>
                    <ArchiveRestore /> Unarchive
                  </DropdownMenuItem>
                ) : (
                  <DropdownMenuItem onSelect={() => void setStatus(project, "archived")}>
                    <Archive /> Archive
                  </DropdownMenuItem>
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onSelect={() => setDel(true)}>
                  <Trash2 /> Delete project…
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        }
      >
        <FolderClosed className="size-4 shrink-0 text-muted-foreground" />
        <PageTitle>{project.name}</PageTitle>
        <ProjectStatusBadge status={project.status} />
      </PageHeader>
      <PageBody width="max-w-[57.5rem]" className="space-y-8">
        <div className="space-y-4">
          <p className={cn("max-w-[70ch] text-[0.875rem] leading-relaxed whitespace-pre-wrap", !project.brief && "text-muted-foreground")}>
            {project.brief || "No brief yet."}
          </p>
          <dl className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border sm:grid-cols-4">
            {[
              ["Agents", `${all.length}`, `${active} active`],
              ["Turns", `${turns}`, "all agents"],
              ["Created", relativeTime(project.created_at), absoluteTime(project.created_at)],
              ["Updated", relativeTime(project.updated_at), absoluteTime(project.updated_at)],
            ].map(([k, v, sub]) => (
              <div key={k} className="bg-card px-4 py-3">
                <dt className="text-2xs font-medium text-subtle-foreground uppercase">{k}</dt>
                <dd className="tabular mt-1 text-[0.9375rem] font-medium">{v}</dd>
                <dd className="tabular truncate text-2xs text-muted-foreground">{sub}</dd>
              </div>
            ))}
          </dl>
          <div className="flex min-w-0 items-center gap-2 rounded-lg border bg-muted/30 py-1 pr-1 pl-3">
            <FolderClosed className="size-3.5 shrink-0 text-muted-foreground" />
            <Truncate className="flex-1 font-mono text-[0.7812rem] text-foreground/85" title={project.folder}>
              {project.folder}
            </Truncate>
            <CopyButton text={project.folder} label="Copy folder path" />
          </div>
        </div>

        <Section title="Agents" right={<AgentListFilter />}>
          {all.length === 0 ? (
            <p className="rounded-xl border bg-card px-4 py-6 text-center text-sm text-muted-foreground">No agents in this project.</p>
          ) : (
            <div className="divide-y overflow-hidden rounded-xl border bg-card">
              {lead && <AgentLine agent={lead} />}
              {workers.map((w) => (
                <AgentLine key={w.id} agent={w} nested={!!lead} />
              ))}
            </div>
          )}
        </Section>
      </PageBody>
      <DeleteProjectDialog project={project} open={del} onOpenChange={setDel} />
      <NewAgentDialog project={project} open={newAgent} onOpenChange={setNewAgent} />
    </div>
  );
}
