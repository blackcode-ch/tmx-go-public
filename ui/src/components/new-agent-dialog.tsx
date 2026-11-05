// "New agent" in a project: name, harness (installed or not, chat or terminal only), mode,
// profile, model, role and brief. The brief is also the agent's first message.
import { useEffect, useMemo, useState } from "react";
import { Eye, Loader2, MessagesSquare, SquareTerminal } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import { modelName } from "@/lib/format";
import { FALLBACK_HARNESSES } from "@/lib/harness";
import { useHarnessAccounts } from "@/lib/accounts";
import { sortProfiles } from "@/lib/profiles";
import { navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import type { Harness, HarnessInfo, Mode, Project } from "@/lib/types";
import { cn } from "@/lib/utils";

const NAME_RX = /^[a-z0-9][a-z0-9-]{0,39}$/;

const MODE_HINT: Record<Mode, string> = {
  chat: "Structured: tmx reads its turns, tool calls and permission requests.",
  terminal: "Its own interface in a terminal. tmx reads the screen for its state and menus, and types messages in when it is idle.",
};

function Tag({ children, tone = "muted" }: { children: React.ReactNode; tone?: "muted" | "ok" | "warn" }) {
  return (
    <span
      className={cn(
        "inline-flex h-[1.125rem] items-center rounded-[5px] px-1.5 text-2xs font-medium whitespace-nowrap",
        tone === "ok" && "bg-state-ok/12 text-[color-mix(in_oklch,var(--state-ok)_70%,var(--foreground))]",
        tone === "warn" && "bg-muted text-subtle-foreground",
        tone === "muted" && "bg-muted text-muted-foreground",
      )}
    >
      {children}
    </span>
  );
}

function HarnessOption({ h, selected, onSelect }: { h: HarnessInfo; selected: boolean; onSelect: () => void }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      disabled={!h.installed}
      onClick={onSelect}
      title={h.installed ? h.path : `Not installed here${h.install ? `: ${h.install}` : ""}`}
      className={cn(
        "flex min-w-0 flex-col items-start gap-1.5 rounded-lg border px-3 py-2.5 text-left outline-none transition-colors duration-150 focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-55",
        selected ? "border-brand/50 bg-brand-soft" : "border-border hover:bg-muted/50",
      )}
    >
      <span className="text-[0.8125rem] font-medium">{h.label}</span>
      <span className="flex flex-wrap gap-1">
        <Tag>{h.structured ? "Chat + terminal" : "Terminal only"}</Tag>
        {h.installed ? <Tag tone="ok">Installed</Tag> : <Tag tone="warn">Not installed</Tag>}
      </span>
    </button>
  );
}

export function NewAgentDialog({ project, open, onOpenChange }: { project: Project; open: boolean; onOpenChange: (v: boolean) => void }) {
  const profiles = useStore((s) => s.profiles);
  const hasLead = useStore((s) => !!project.lead_id && !!s.agents[project.lead_id]);
  const [harnesses, setHarnesses] = useState<HarnessInfo[]>(FALLBACK_HARNESSES);
  const [name, setName] = useState("");
  const [harness, setHarness] = useState<Harness>("claude");
  const [mode, setMode] = useState<Mode>("chat");
  const [profile, setProfile] = useState("worker");
  const [model, setModel] = useState("");
  const [role, setRole] = useState("");
  const [brief, setBrief] = useState("");
  const [agyFullAccess, setAgyFullAccess] = useState(false);
  const [account, setAccount] = useState(""); // "" = the harness's default for new agents
  const harnessAccounts = useHarnessAccounts(harness);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    setName("");
    setHarness("claude");
    setMode("chat");
    setProfile("worker");
    setModel("");
    setRole("");
    setBrief("");
    setAgyFullAccess(false);
    setError("");
    setBusy(false);
    let live = true;
    api
      .harnesses()
      .then((hs) => live && hs?.length && setHarnesses(hs))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [open]);

  const info = harnesses.find((h) => h.name === harness) ?? harnesses[0];
  const choices = useMemo(
    () => sortProfiles(Object.values(profiles)).filter((p) => p.level === "worker" || (p.level === "lead" && !hasLead)),
    [profiles, hasLead],
  );
  const prof = profiles[profile];
  const claudeModels = Array.from(new Set([prof?.model ?? "", ...(prof?.allowed_models ?? [])].filter(Boolean)));
  const isClaude = harness === "claude";
  const isShell = harness === "shell";
  const shownModel = isClaude ? model || prof?.model || "" : model;
  const readOnlyOnPi = harness === "pi" && !!prof?.read_only;
  const nameOK = NAME_RX.test(name);
  const canCreate = nameOK && !!info?.installed && !readOnlyOnPi && !busy;

  function pickHarness(h: HarnessInfo) {
    setHarness(h.name);
    setAccount("");
    setMode(h.modes.includes(mode) ? mode : h.modes[0]);
    setModel("");
  }

  async function create(e: React.FormEvent) {
    e.preventDefault();
    if (!canCreate) return;
    setBusy(true);
    setError("");
    try {
      const a = await api.createAgent({
        name,
        project_id: project.id,
        profile,
        harness,
        mode,
        model: isShell ? undefined : isClaude ? (model && model !== prof?.model ? model : undefined) : model.trim() || undefined,
        role: role.trim() || undefined,
        brief: brief.trim() || undefined,
        agy_full_access: (harness === "agy" || harness === "antigravity") && agyFullAccess || undefined,
        account: account || undefined,
      });
      onOpenChange(false);
      toast.success(`Started ${a.name}`, { description: `${info.label} in ${mode} mode` });
      navigate(paths.agent(a.id));
    } catch (err) {
      setError((err as Error).message || "The agent wasn't created.");
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !busy && onOpenChange(v)}>
      <DialogContent className="max-h-[92dvh] overflow-y-auto sm:max-w-[37.5rem]">
        <form onSubmit={create} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>New agent in {project.name}</DialogTitle>
            <DialogDescription>It starts in the project folder{hasLead ? " and reports to the lead" : ""}.</DialogDescription>
          </DialogHeader>

          <div className="grid gap-1.5">
            <Label htmlFor="na-name">Name</Label>
            <Input
              id="na-name"
              value={name}
              onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))}
              placeholder="api-tests"
              autoFocus
              autoComplete="off"
              spellCheck={false}
              aria-invalid={!!name && !nameOK}
              className="font-mono text-[0.8125rem]"
            />
            {!!name && !nameOK && <p className="text-2xs text-state-error">Lowercase letters, digits and dashes, up to 40.</p>}
          </div>

          <div className="grid gap-1.5">
            <span className="text-sm font-medium">Harness</span>
            <div role="radiogroup" aria-label="Harness" className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              {harnesses.map((h) => (
                <HarnessOption key={h.name} h={h} selected={h.name === harness} onSelect={() => pickHarness(h)} />
              ))}
            </div>
          </div>

          <div className="grid gap-1.5">
            <span className="text-sm font-medium">Mode</span>
            <div role="radiogroup" aria-label="Mode" className="inline-flex w-fit rounded-lg border bg-muted/40 p-0.5">
              {(["chat", "terminal"] as Mode[]).map((m) => {
                const ok = info?.modes.includes(m);
                const Icon = m === "chat" ? MessagesSquare : SquareTerminal;
                return (
                  <button
                    key={m}
                    type="button"
                    role="radio"
                    aria-checked={mode === m}
                    disabled={!ok}
                    onClick={() => setMode(m)}
                    className={cn(
                      "flex h-7 items-center gap-1.5 rounded-md px-3 text-[0.7812rem] text-muted-foreground outline-none transition-colors duration-150 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-45 disabled:hover:text-muted-foreground",
                      mode === m && "bg-background font-medium text-foreground shadow-sm dark:bg-accent",
                    )}
                  >
                    <Icon className="size-3.5" />
                    {m === "chat" ? "Chat" : "Terminal"}
                  </button>
                );
              })}
            </div>
            <p className="text-2xs text-muted-foreground">
              {MODE_HINT[mode]}
              {!info?.structured && !isShell && ` ${info?.label} has no chat mode.`}
            </p>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid content-start gap-1.5">
              <Label>Profile</Label>
              <Select
                value={profile}
                onValueChange={(v) => {
                  setProfile(v);
                  setModel("");
                }}
              >
                <SelectTrigger className="w-full" aria-label="Profile">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {choices.map((p) => (
                    <SelectItem key={p.slug} value={p.slug}>
                      {p.read_only && <Eye className="size-3.5" />}
                      <span>{p.name}</span>
                      <span className="font-mono text-2xs text-muted-foreground">{p.level === "lead" ? "lead" : p.slug}</span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {readOnlyOnPi ? (
                <p className="text-2xs text-state-error">pi cannot be limited to reading; pick another harness or profile.</p>
              ) : (
                prof?.description && <p className="text-2xs text-muted-foreground">{prof.description}</p>
              )}
            </div>
            <div className="grid content-start gap-1.5">
              <Label htmlFor="na-model">Model</Label>
              {isShell ? (
                <Input id="na-model" value="" disabled placeholder="A shell has no model" />
              ) : isClaude ? (
                <Select value={shownModel} onValueChange={setModel}>
                  <SelectTrigger id="na-model" className="w-full" aria-label="Model">
                    <SelectValue placeholder="The profile's model" />
                  </SelectTrigger>
                  <SelectContent>
                    {claudeModels.map((m) => (
                      <SelectItem key={m} value={m}>
                        <span>{modelName(m)}</span>
                        <span className="font-mono text-2xs text-muted-foreground">{m}</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <Input
                  id="na-model"
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                  placeholder={`${info?.label ?? "The harness"}'s default`}
                  className="font-mono text-[0.7812rem]"
                  spellCheck={false}
                />
              )}
              {!isShell && (
                <p className="text-2xs text-muted-foreground">
                  {isClaude ? "Models this profile allows." : "Empty uses the harness's own default."}
                </p>
              )}
            </div>
          </div>

          {harnessAccounts.length > 1 && (
            <div className="grid gap-1.5">
              <Label htmlFor="na-account">Account</Label>
              <Select value={account || (harnessAccounts.find((a) => a.launch_default)?.name ?? "default")} onValueChange={setAccount}>
                <SelectTrigger id="na-account" className="w-full" aria-label="Account">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {harnessAccounts.map((a) => (
                    <SelectItem key={a.name} value={a.name}>
                      <span>{a.name}</span>
                      <span className="text-2xs text-muted-foreground">
                        {[a.connected ? a.account?.email || "signed in" : "not signed in", a.launch_default ? "default" : ""].filter(Boolean).join(" · ")}
                      </span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-2xs text-muted-foreground">Which of your {info?.label ?? "harness"} accounts this agent runs as.</p>
            </div>
          )}

          <div className="grid gap-1.5">
            {(harness === "agy" || harness === "antigravity") && <label className="flex items-start gap-2 text-sm">
              <input type="checkbox" checked={agyFullAccess} disabled={prof?.read_only} onChange={(e) => setAgyFullAccess(e.target.checked)} />
              <span>Skip this agent's permission prompts. Requires owner confirmation when the gate is on; resets when tmx restarts.</span>
            </label>}
            <Label htmlFor="na-role">Role</Label>
            <Input id="na-role" value={role} onChange={(e) => setRole(e.target.value)} placeholder="e.g. backend, tests, docs" />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="na-brief">Brief</Label>
            <Textarea
              id="na-brief"
              value={brief}
              onChange={(e) => setBrief(e.target.value)}
              placeholder="What should this agent do? Sent as its first message too."
              className="min-h-20 resize-none"
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void create(e);
              }}
            />
          </div>

          {error && <p className="rounded-md border border-state-error/30 bg-diff-del-bg px-3 py-2 text-xs text-foreground">{error}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" disabled={!canCreate}>
              {busy && <Loader2 className="animate-spin" />}
              Start agent
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
