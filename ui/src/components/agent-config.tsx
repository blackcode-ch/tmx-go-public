// Agent → Details: the owner edits role, brief, model and profile (PATCH /api/agents/{id}).
// The daemon applies them at the agent's next start, so a running agent offers "Restart now".
import { useEffect, useMemo, useState } from "react";
import { Eye, Info, Loader2, RotateCw } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import { isRunning, modelName } from "@/lib/format";
import { profileOf, sortProfiles } from "@/lib/profiles";
import { href, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import type { Agent } from "@/lib/types";

interface Draft {
  protected: boolean;
  role: string;
  brief: string;
  model: string;
  profile: string;
}

function Field({ label, htmlFor, children, hint }: { label: string; htmlFor?: string; children: React.ReactNode; hint?: React.ReactNode }) {
  return (
    <div className="grid grid-cols-1 items-start gap-1.5 py-3 sm:grid-cols-[180px_1fr] sm:gap-4">
      <label htmlFor={htmlFor} className="pt-1.5 text-sm text-muted-foreground">
        {label}
      </label>
      <div className="min-w-0">
        {children}
        {hint && <p className="mt-1.5 text-xs text-muted-foreground">{hint}</p>}
      </div>
    </div>
  );
}

export function AgentConfig({ agent }: { agent: Agent }) {
  const profiles = useStore((s) => s.profiles);
  const current = profileOf(agent, profiles);
  const base = useMemo<Draft>(
    () => ({ protected: !!agent.protected, role: agent.role, brief: agent.brief, model: agent.model, profile: current?.slug ?? agent.profile }),
    [agent.protected, agent.role, agent.brief, agent.model, agent.profile, current?.slug],
  );
  const [draft, setDraft] = useState<Draft>(base);
  const [saving, setSaving] = useState(false);
  const [pending, setPending] = useState(""); // note of a save that waits for a restart
  useEffect(() => setDraft(base), [base]);
  // A restart picks the changes up.
  useEffect(() => {
    if (agent.state === "starting") setPending("");
  }, [agent.state]);

  const choices = useMemo(() => sortProfiles(Object.values(profiles)).filter((p) => p.level === agent.level), [profiles, agent.level]);
  const chosen = profiles[draft.profile] ?? current;
  const models = Array.from(new Set([chosen?.model ?? "", ...(chosen?.allowed_models ?? [])].filter(Boolean)));
  const shell = agent.harness === "shell";
  const freeModel = agent.harness !== "claude" && !shell; // codex, opencode, pi: their own model names
  const dirty = JSON.stringify(draft) !== JSON.stringify(base);
  const modelOK = shell || freeModel || models.includes(draft.model);

  async function save() {
    setSaving(true);
    try {
      const body: Partial<Draft> = {};
      if (draft.protected !== base.protected) body.protected = draft.protected;
      if (draft.role !== base.role) body.role = draft.role;
      if (draft.brief !== base.brief) body.brief = draft.brief;
      if (draft.profile !== base.profile) body.profile = draft.profile;
      if (!shell && (draft.model !== base.model || body.profile)) body.model = draft.model;
      const res = await api.patchAgent(agent.id, body);
      if (res?.agent) useStore.setState((st) => ({ agents: { ...st.agents, [res.agent.id]: res.agent } }));
      if (res?.restart_needed) setPending(res.note || "Saved; applies on restart.");
      else toast.success(res?.note || "Saved");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function restart() {
    try {
      await api.agentAction(agent.id, "restart");
      setPending("");
      toast(`Restarting ${agent.name}`);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <section className="mb-8">
      <div className="mb-1 flex h-7 items-center gap-2">
        <h2 className="text-[0.8125rem] font-medium">Configuration</h2>
        <span className="text-xs text-muted-foreground">Applies when the agent starts or restarts.</span>
      </div>
      <div className="divide-y border-y">
        {agent.level === "worker" && !shell && <Field label="Keep available" hint="Never automatically shelve this agent. Takes effect when saved.">
          <input aria-label="Protect from automatic shelving" type="checkbox" checked={draft.protected} onChange={(e) => setDraft((d) => ({ ...d, protected: e.target.checked }))} />
        </Field>}
        <Field
          label="Profile"
          hint={
            chosen && (
              <>
                {chosen.description || "No description."}{" "}
                <a className="text-brand hover:underline" href={href(paths.profile(chosen.slug))}>
                  Edit profile
                </a>
              </>
            )
          }
        >
          <Select
            value={draft.profile}
            onValueChange={(v) => {
              const p = profiles[v];
              setDraft((d) => ({ ...d, profile: v, model: shell || freeModel ? d.model : (p?.model ?? d.model) }));
            }}
            disabled={choices.length < 2}
          >
            <SelectTrigger className="w-full sm:w-72" aria-label="Profile">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {choices.map((p) => (
                <SelectItem key={p.slug} value={p.slug}>
                  {p.read_only && <Eye className="size-3.5" />}
                  <span>{p.name}</span>
                  <span className="font-mono text-2xs text-muted-foreground">{p.slug}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field
          label="Model"
          htmlFor={freeModel ? "cfg-model" : undefined}
          hint={shell ? "A shell has no model." : freeModel ? "Empty uses the harness's own default." : !modelOK ? "Not allowed by this profile: pick one of its models." : undefined}
        >
          {freeModel ? (
            <Input
              id="cfg-model"
              value={draft.model}
              onChange={(e) => setDraft((d) => ({ ...d, model: e.target.value.trim() }))}
              placeholder="The harness's default"
              className="font-mono text-[0.7812rem] sm:w-72"
              spellCheck={false}
            />
          ) : (
          <Select value={shell ? "__none" : draft.model} onValueChange={(v) => setDraft((d) => ({ ...d, model: v }))} disabled={shell}>
            <SelectTrigger className="w-full sm:w-72" aria-label="Model" aria-invalid={!modelOK}>
              <SelectValue placeholder="Pick a model" />
            </SelectTrigger>
            <SelectContent>
              {shell && <SelectItem value="__none">Shell</SelectItem>}
              {!shell && !models.includes(draft.model) && draft.model && (
                <SelectItem value={draft.model} disabled>
                  <span>{modelName(draft.model)}</span>
                  <span className="font-mono text-2xs text-muted-foreground">not allowed</span>
                </SelectItem>
              )}
              {models.map((m) => (
                <SelectItem key={m} value={m}>
                  <span>{modelName(m)}</span>
                  <span className="font-mono text-2xs text-muted-foreground">{m}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          )}
        </Field>
        <Field label="Role" htmlFor="cfg-role">
          <Input id="cfg-role" value={draft.role} onChange={(e) => setDraft((d) => ({ ...d, role: e.target.value }))} placeholder="e.g. backend, tests, docs" />
        </Field>
        <Field label="Brief" htmlFor="cfg-brief">
          <Textarea
            id="cfg-brief"
            value={draft.brief}
            onChange={(e) => setDraft((d) => ({ ...d, brief: e.target.value }))}
            placeholder="Standing brief kept in the agent's instructions"
            className="min-h-20 resize-y"
          />
        </Field>
      </div>
      {(dirty || pending) && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {pending && !dirty ? (
            <>
              <span className="inline-flex items-center gap-1.5 text-sm">
                <Info className="size-4 text-brand" />
                {pending}
              </span>
              {isRunning(agent.state) && (
                <Button size="sm" variant="outline" className="ml-auto" onClick={() => void restart()}>
                  <RotateCw />
                  Restart now
                </Button>
              )}
            </>
          ) : (
            <div className="ml-auto flex items-center gap-1.5">
              <Button variant="ghost" size="sm" onClick={() => setDraft(base)} disabled={saving}>
                Discard
              </Button>
              <Button size="sm" onClick={() => void save()} disabled={saving || !modelOK}>
                {saving && <Loader2 className="animate-spin" />}
                Save
              </Button>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
