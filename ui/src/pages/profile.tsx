// Settings → Profiles → one profile: the editor (also "new").
import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, ChevronRight, Ellipsis, Info, Loader2, RotateCcw, Trash2, UserRoundCog } from "lucide-react";
import { toast } from "sonner";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { LevelBadge } from "@/components/badges";
import { EmptyState, PageBody, PageHeader, Section } from "@/components/page";
import { InstructionsEditor, InstructionsPreview, TokenField, ToggleChip } from "@/components/profile-fields";
import { api } from "@/lib/api";
import { LEVEL_LABEL, modelName, plural, slugify } from "@/lib/format";
import { cleanList, profileOf, READ_ONLY_TOOLS, sortProfiles } from "@/lib/profiles";
import { href, navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import type { Agent, Level, Profile } from "@/lib/types";
import { KNOWN_MODELS, ModelPicker, MODES, Row } from "@/pages/settings";

const LEVELS: Level[] = ["head", "lead", "worker"];
const LEVEL_HINT: Record<Level, string> = {
  head: "The machine's one head: talks with you and creates projects.",
  lead: "Owns a project and launches its workers.",
  worker: "Does the work in a project and reports to its lead.",
};
const SLUG_RX = /^[a-z0-9][a-z0-9-]{0,39}$/;

type Draft = Pick<
  Profile,
  | "slug"
  | "name"
  | "description"
  | "level"
  | "instructions"
  | "model"
  | "allowed_models"
  | "permission_mode"
  | "allowed_tools"
  | "disallowed_tools"
  | "read_only"
  | "can_create_projects"
  | "can_launch"
  | "companion_tasks"
>;

function toDraft(p: Profile): Draft {
  return {
    slug: p.slug,
    name: p.name,
    description: p.description,
    level: p.level,
    instructions: p.instructions,
    model: p.model,
    allowed_models: p.allowed_models ?? [],
    permission_mode: p.permission_mode || "auto",
    allowed_tools: p.allowed_tools ?? [],
    disallowed_tools: p.disallowed_tools ?? [],
    read_only: p.read_only,
    can_create_projects: p.can_create_projects,
    can_launch: p.can_launch ?? [],
    companion_tasks: !!p.companion_tasks,
  };
}

function newDraft(base: Profile | undefined): Draft {
  return {
    slug: "",
    name: "",
    description: "",
    level: "worker",
    instructions:
      base?.instructions ??
      'You are "{{name}}", a worker in project "{{project}}", run by tmx; your folder is {{folder}}. Your lead is {{parent}}.',
    model: base?.model ?? "claude-sonnet-5",
    allowed_models: base?.allowed_models ?? [],
    permission_mode: base?.permission_mode ?? "auto",
    allowed_tools: [],
    disallowed_tools: [],
    read_only: false,
    can_create_projects: false,
    can_launch: [],
    companion_tasks: false,
  };
}

/** Values an example agent of this profile would get, for the preview. */
function exampleVars(
  d: Draft,
  agent: Agent | undefined,
  ctx: { machine: string; workspace: string; projects: ReturnType<typeof useStore.getState>["projects"]; agents: Record<string, Agent>; users: Agent[] },
) {
  const sep = ctx.workspace.includes("\\") && !ctx.workspace.includes("/") ? "\\" : "/";
  // A user's project, else the newest active one.
  const firstProject =
    ctx.projects[ctx.users.find((a) => a.project_id)?.project_id ?? ""] ??
    Object.values(ctx.projects)
      .filter((p) => p.status === "active")
      .sort((a, b) => b.created_at - a.created_at)[0];
  const project = agent?.project_id ? ctx.projects[agent.project_id] : d.level === "head" ? undefined : firstProject;
  const folder = agent?.cwd ?? (project ? project.folder : ctx.workspace);
  const parent = agent?.parent_id
    ? (ctx.agents[agent.parent_id]?.name ?? agent.parent_id)
    : d.level === "head"
      ? "the owner"
      : d.level === "lead"
        ? "head"
        : project
          ? `${project.slug}-lead`
          : "head";
  const name = agent?.name ?? (d.level === "head" ? "head" : d.level === "lead" ? `${project?.slug ?? "app"}-lead` : `${d.slug || "example"}-1`);
  return {
    name,
    level: d.level,
    profile: d.name || "New profile",
    project: project?.name ?? "(none)",
    project_folder: project?.folder ?? folder,
    folder: folder || `${ctx.workspace}${sep}example`,
    parent,
    machine: ctx.machine,
    workspace: ctx.workspace,
    model: agent?.model ?? d.model,
  };
}

function Card({ children }: { children: React.ReactNode }) {
  return <div className="divide-y rounded-xl border bg-card px-4">{children}</div>;
}

export function ProfilePage({ slug }: { slug: string }) {
  const isNew = slug === "new";
  const profiles = useStore((s) => s.profiles);
  const agents = useStore((s) => s.agents);
  const projects = useStore((s) => s.projects);
  const machine = useStore((s) => s.machine);
  const workspace = useStore((s) => s.workspace);
  const status = useStore((s) => s.status);
  const profile = isNew ? undefined : profiles[slug];

  const base = useMemo(() => (profile ? toDraft(profile) : newDraft(profiles.worker)), [profile, profiles.worker]);
  const [draft, setDraft] = useState<Draft>(base);
  const [slugEdited, setSlugEdited] = useState(false);
  const [saving, setSaving] = useState(false);
  const [confirm, setConfirm] = useState<"" | "reset" | "delete">("");
  const [previewAs, setPreviewAs] = useState("__example");
  useEffect(() => setDraft(base), [base]);

  const dirty = useMemo(() => JSON.stringify(draft) !== JSON.stringify(base), [draft, base]);
  const users = useMemo(
    () => (profile ? Object.values(agents).filter((a) => profileOf(a, profiles)?.slug === profile.slug).sort((a, b) => a.name.localeCompare(b.name)) : []),
    [agents, profiles, profile],
  );
  const running = users.filter((a) => a.state !== "stopped" && a.state !== "exited" && a.state !== "error").length;
  const others = useMemo(() => sortProfiles(Object.values(profiles)).filter((p) => p.slug !== slug && p.level !== "head"), [profiles, slug]);

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => ({ ...d, [k]: v }));
  const effSlug = isNew ? (slugEdited ? draft.slug : slugify(draft.name)) : slug;
  const allowed = draft.allowed_models ?? [];
  const modelOptions = Array.from(new Set([...KNOWN_MODELS, ...allowed, draft.model].filter(Boolean)));
  const errors = [
    !draft.name.trim() && "a name",
    isNew && !SLUG_RX.test(effSlug) && "a slug of lowercase letters, digits and dashes",
    isNew && profiles[effSlug] && `a slug other than ${effSlug} (taken)`,
    !draft.model.trim() && "a default model",
  ].filter(Boolean) as string[];

  if (status !== "ready") return <div className="flex-1" />;
  if (!isNew && !profile) {
    return (
      <div className="flex flex-1 items-center justify-center p-6">
        <EmptyState icon={UserRoundCog} title="No such profile" className="w-full max-w-md border-none bg-transparent">
          It may have been deleted.
          <div className="mt-4">
            <Button variant="outline" size="sm" asChild>
              <a href={href(paths.settings())}>Back to settings</a>
            </Button>
          </div>
        </EmptyState>
      </div>
    );
  }

  const previewAgent = previewAs === "__example" ? undefined : agents[previewAs];
  const vars = exampleVars(draft, previewAgent, { machine, workspace, projects, agents, users });

  async function save() {
    setSaving(true);
    const body: Draft = {
      ...draft,
      slug: effSlug,
      allowed_models: cleanList([draft.model, ...allowed]),
    };
    try {
      if (isNew) {
        const p = await api.createProfile(body);
        if (p) useStore.setState((st) => ({ profiles: { ...st.profiles, [p.slug]: p } }));
        toast.success(`Created ${body.name.trim()}`, { description: "Leads and the head can launch it once you allow it under Launch rights." });
        navigate(paths.profile(effSlug), { replace: true });
      } else {
        const p = await api.putProfile(slug, body);
        if (p) useStore.setState((st) => ({ profiles: { ...st.profiles, [p.slug]: p } }));
        toast.success("Profile saved", {
          description: running ? `${plural(running, "running agent")} pick it up when restarted.` : "Agents pick it up when they start.",
        });
      }
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function reset() {
    try {
      const p = await api.resetProfile(slug);
      if (p) useStore.setState((st) => ({ profiles: { ...st.profiles, [p.slug]: p } }));
      toast(`${profile?.name} reset to its default`);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function remove() {
    try {
      await api.deleteProfile(slug);
      useStore.setState((st) => {
        const next = { ...st.profiles };
        delete next[slug];
        return { profiles: next };
      });
      toast(`Deleted ${profile?.name}`);
      navigate(paths.settings());
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  const mode = MODES.find((m) => m.value === draft.permission_mode);
  const title = isNew ? draft.name.trim() || "New profile" : profile!.name;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        actions={
          <>
            {(dirty || isNew) && (
              <>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={saving}
                  onClick={() => (isNew ? navigate(paths.settings()) : setDraft(base))}
                >
                  {isNew ? "Cancel" : "Discard"}
                </Button>
                <Button size="sm" onClick={() => void save()} disabled={saving || errors.length > 0} title={errors.length ? `Needs ${errors.join(", ")}` : undefined}>
                  {saving && <Loader2 className="animate-spin" />}
                  {isNew ? "Create profile" : "Save changes"}
                </Button>
              </>
            )}
            {!isNew && (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon-sm" aria-label="Profile actions" className="text-muted-foreground">
                    <Ellipsis className="size-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-52">
                  {profile!.built_in ? (
                    <DropdownMenuItem disabled={!profile!.modified} onSelect={() => setConfirm("reset")}>
                      <RotateCcw /> Reset to default
                    </DropdownMenuItem>
                  ) : (
                    <DropdownMenuItem variant="destructive" disabled={users.length > 0} onSelect={() => setConfirm("delete")}>
                      <Trash2 /> {users.length > 0 ? "In use: can't delete" : "Delete profile…"}
                    </DropdownMenuItem>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </>
        }
      >
        <a href={href(paths.settings())} className="hidden shrink-0 text-[0.8125rem] text-muted-foreground hover:text-foreground sm:block">
          Settings
        </a>
        <ChevronRight className="hidden size-3.5 shrink-0 text-subtle-foreground sm:block" />
        <h1 className="min-w-0 truncate text-[0.875rem] font-semibold tracking-tight">{title}</h1>
        <LevelBadge level={draft.level} />
        {profile?.built_in && (
          <span className="hidden h-5 items-center rounded-[5px] bg-muted px-1.5 text-2xs font-medium text-muted-foreground sm:inline-flex">
            {profile.modified ? "Built-in · edited" : "Built-in"}
          </span>
        )}
      </PageHeader>

      <PageBody width="max-w-[61.25rem]" className="space-y-8">
        {!isNew && users.length > 0 && (
          <div className="flex items-start gap-2 rounded-lg border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
            <Info className="mt-px size-3.5 shrink-0" />
            <p>
              Used by{" "}
              {users.slice(0, 6).map((a, i) => (
                <span key={a.id}>
                  {i > 0 && ", "}
                  <a className="text-brand hover:underline" href={href(paths.agent(a.id, "details"))}>
                    {a.name}
                  </a>
                </span>
              ))}
              {users.length > 6 && ` and ${users.length - 6} more`}. Changes apply when an agent starts or restarts.
            </p>
          </div>
        )}

        <Section title="General">
          <Card>
            <Row label="Name">
              <Input value={draft.name} onChange={(e) => set("name", e.target.value)} placeholder="Reviewer" className="sm:w-72" autoFocus={isNew} />
            </Row>
            <Row label="Slug" hint={isNew ? "Used in agent_launch profile=…; can't change later." : "Agents and launch rights refer to it."}>
              {isNew ? (
                <Input
                  value={effSlug}
                  onChange={(e) => {
                    setSlugEdited(true);
                    set("slug", e.target.value.trim());
                  }}
                  placeholder="reviewer"
                  className="font-mono text-[0.7812rem] sm:w-72"
                  spellCheck={false}
                  aria-invalid={!!effSlug && (!SLUG_RX.test(effSlug) || !!profiles[effSlug])}
                />
              ) : (
                <p className="pt-1.5 font-mono text-[0.7812rem]">{slug}</p>
              )}
            </Row>
            <Row label="Description" hint="Shown to the agents that may launch it.">
              <Textarea
                value={draft.description}
                onChange={(e) => set("description", e.target.value)}
                placeholder="What this kind of agent is for"
                className="min-h-14 resize-none"
              />
            </Row>
            <Row
              label="Level"
              hint={
                profile?.built_in
                  ? "Built-in profiles keep their level."
                  : users.length
                    ? "Fixed while agents use this profile."
                    : "Keeps the rights of its place in the tree."
              }
            >
              <Select value={draft.level} onValueChange={(v) => set("level", v as Level)} disabled={!!profile?.built_in || users.length > 0}>
                <SelectTrigger className="w-full sm:w-72">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {LEVELS.map((l) => (
                    <SelectItem key={l} value={l}>
                      {LEVEL_LABEL[l]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="mt-1.5 text-xs text-muted-foreground">{LEVEL_HINT[draft.level]}</p>
            </Row>
          </Card>
        </Section>

        <Section title="Model and permissions">
          <Card>
            <Row label="Default model" hint="Used when the launcher doesn't name one.">
              <ModelPicker value={draft.model} onChange={(m) => set("model", m)} />
            </Row>
            <Row label="Allowed models" hint="The daemon refuses any other model at launch.">
              <div className="flex flex-wrap gap-1.5">
                {modelOptions.map((m) => {
                  const on = m === draft.model || allowed.includes(m);
                  return (
                    <ToggleChip
                      key={m}
                      pressed={on}
                      locked={m === draft.model}
                      title={m === draft.model ? "The default model is always allowed" : m}
                      onToggle={() => set("allowed_models", on ? allowed.filter((x) => x !== m) : [...allowed, m])}
                    >
                      {modelName(m)}
                      {m === draft.model && <span className="text-2xs text-subtle-foreground">default</span>}
                    </ToggleChip>
                  );
                })}
              </div>
            </Row>
            <Row label="Permissions" hint={mode?.hint}>
              <Select value={draft.permission_mode} onValueChange={(v) => set("permission_mode", v)}>
                <SelectTrigger className="w-full sm:w-72">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {MODES.map((m) => (
                    <SelectItem key={m.value} value={m.value}>
                      {m.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {draft.permission_mode === "bypassPermissions" && (
                <div className="mt-2 flex items-start gap-2 rounded-lg border border-state-error/30 bg-diff-del-bg px-3 py-2 text-xs">
                  <AlertTriangle className="mt-px size-3.5 shrink-0 text-state-error" />
                  <span>
                    Agents of this profile will run commands, edit files and reach the network without asking you. Use this only in a throwaway folder or a
                    sandbox.
                  </span>
                </div>
              )}
            </Row>
          </Card>
        </Section>

        <Section title="Tools">
          <Card>
            <Row label="Read-only" hint="No shell and no file edits.">
              <div className="flex items-start gap-3 pt-1">
                <Switch checked={draft.read_only} onCheckedChange={(v) => set("read_only", v)} aria-label="Read-only" className="mt-0.5" />
                <p className="text-xs leading-5 text-muted-foreground">
                  Disallows{" "}
                  {READ_ONLY_TOOLS.map((t, i) => (
                    <span key={t}>
                      <code className="rounded-[4px] bg-muted px-1 py-px font-mono text-[0.6875rem] text-foreground/80">{t}</code>
                      {i < READ_ONLY_TOOLS.length - 1 ? " " : ""}
                    </span>
                  ))}
                  . The tmx tools always work.
                </p>
              </div>
            </Row>
            <Row label="Allowed tools" hint="Run without asking (claude --allowedTools). The tmx tools are always allowed.">
              <TokenField
                value={draft.allowed_tools ?? []}
                onChange={(v) => set("allowed_tools", v)}
                placeholder="Read, Grep, Bash(git status:*) …"
                validate={(s) => (s.includes(",") ? "One tool or rule per entry." : "")}
              />
            </Row>
            <Row label="Disallowed tools" hint="Removed from the agent (claude --disallowedTools).">
              <TokenField
                value={draft.disallowed_tools ?? []}
                onChange={(v) => set("disallowed_tools", v)}
                placeholder="WebFetch, Agent …"
                validate={(s) => (s.includes(",") ? "One tool or rule per entry." : "")}
              />
            </Row>
          </Card>
        </Section>

        <Section title="Launch rights">
          <Card>
            <Row label="Create projects" hint="project_create: a folder and its lead.">
              <div className="pt-1">
                <Switch checked={draft.can_create_projects} onCheckedChange={(v) => set("can_create_projects", v)} aria-label="Create projects" />
              </div>
            </Row>
            <Row
              label="Hand tasks to Companion"
              hint="companion_task: ask Companion, your desktop assistant, to do something in your apps or accounts. You see and allow every hand-off first."
            >
              <div className="pt-1">
                <Switch checked={!!draft.companion_tasks} onCheckedChange={(v) => set("companion_tasks", v)} aria-label="Hand tasks to Companion" />
              </div>
            </Row>
            <Row label="Can launch" hint="Profiles it may start below itself; the daemon enforces this.">
              {others.length === 0 ? (
                <p className="pt-1.5 text-sm text-muted-foreground">No other profiles.</p>
              ) : (
                <div className="flex flex-wrap gap-1.5">
                  {others.map((o) => {
                    const list = draft.can_launch ?? [];
                    const on = list.includes(o.slug);
                    return (
                      <ToggleChip
                        key={o.slug}
                        pressed={on}
                        title={`${o.name} (${LEVEL_LABEL[o.level].toLowerCase()})`}
                        onToggle={() => set("can_launch", on ? list.filter((x) => x !== o.slug) : [...list, o.slug])}
                      >
                        {o.name}
                        {o.name !== LEVEL_LABEL[o.level] && <span className="text-2xs text-subtle-foreground">{LEVEL_LABEL[o.level].toLowerCase()}</span>}
                      </ToggleChip>
                    );
                  })}
                </div>
              )}
            </Row>
          </Card>
        </Section>

        <Section title="Instructions">
          <p className="-mt-1 mb-3 text-xs text-muted-foreground">
            The agent's standing instructions. tmx fills the variables at each start and adds the role, the brief and the mail rules.
          </p>
          <div className="grid gap-4 lg:grid-cols-2">
            <InstructionsEditor value={draft.instructions} onChange={(v) => set("instructions", v)} />
            <InstructionsPreview
              template={draft.instructions}
              vars={vars}
              readOnly={draft.read_only}
              role={previewAgent?.role ?? ""}
              brief={previewAgent?.brief ?? ""}
              header={
                <>
                  <span className="text-xs font-medium">Preview</span>
                  <span className="text-xs text-muted-foreground">as</span>
                  <Select value={previewAs} onValueChange={setPreviewAs}>
                    <SelectTrigger size="sm" className="h-7 max-w-[13.75rem] border-transparent px-2 text-xs shadow-none hover:bg-muted dark:bg-transparent">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="__example">Example {LEVEL_LABEL[draft.level].toLowerCase()}</SelectItem>
                      {users.length > 0 && <SelectSeparator />}
                      {users.map((a) => (
                        <SelectItem key={a.id} value={a.id}>
                          {a.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </>
              }
            />
          </div>
        </Section>
      </PageBody>

      <AlertDialog open={confirm !== ""} onOpenChange={(v) => !v && setConfirm("")}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirm === "reset" ? `Reset ${profile?.name} to its default?` : `Delete ${profile?.name}?`}</AlertDialogTitle>
            <AlertDialogDescription>
              {confirm === "reset"
                ? "Its instructions, model, permissions, tools and launch rights go back to what tmx ships with. Agents pick it up when they restart."
                : "The profile is removed and no longer offered to leads and the head. No agent uses it."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className={confirm === "delete" ? "bg-destructive text-white hover:bg-destructive/90" : undefined}
              onClick={() => void (confirm === "reset" ? reset() : remove())}
            >
              {confirm === "reset" ? "Reset profile" : "Delete profile"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
