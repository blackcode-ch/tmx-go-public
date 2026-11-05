// Settings → Harnesses (what is installed here) and Running outside tmx (GET /api/discover):
// Claude Code, Codex, opencode and pi sessions no tmx started, each adoptable into tmx.
import { useCallback, useEffect, useState } from "react";
import { Fingerprint, Loader2, RefreshCw, Radar, TriangleAlert } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { HarnessBadge } from "@/components/badges";
import { CopyButton } from "@/components/copy-button";
import { Section } from "@/components/page";
import { Truncate } from "@/components/truncate";
import { api } from "@/lib/api";
import { absoluteTime, displayPath, relativeTime, slugify } from "@/lib/format";
import { FALLBACK_HARNESSES } from "@/lib/harness";
import { useGateAsks } from "@/lib/owner-gate";
import { navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import type { DiscoveredAgent, HarnessInfo } from "@/lib/types";

function Tag({ children }: { children: React.ReactNode }) {
  return <span className="inline-flex h-[1.125rem] items-center rounded-[5px] bg-muted px-1.5 text-2xs font-medium text-muted-foreground">{children}</span>;
}

function HarnessRow({ h }: { h: HarnessInfo }) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5">
      <span className="w-28 text-[0.8125rem] font-medium">{h.label}</span>
      <span className="flex gap-1">
        {h.modes.map((m) => (
          <Tag key={m}>{m === "chat" ? "Chat" : "Terminal"}</Tag>
        ))}
      </span>
      <span className="ml-auto flex min-w-0 items-center gap-1 text-xs">
        {h.installed ? (
          h.path ? (
            <Truncate className="max-w-[18.75rem] font-mono text-[0.7188rem] text-muted-foreground" title={h.path}>
              {displayPath(h.path)}
            </Truncate>
          ) : (
            <span className="text-muted-foreground">Built in</span>
          )
        ) : (
          <>
            <span className="text-subtle-foreground">Not installed</span>
            {h.install && (
              <>
                <code className="hidden rounded bg-muted px-1.5 py-0.5 font-mono text-[0.6875rem] text-muted-foreground sm:inline">{h.install}</code>
                <CopyButton text={h.install} label="Copy install command" />
              </>
            )}
          </>
        )}
      </span>
    </div>
  );
}

function conversationLine(d: DiscoveredAgent): string {
  if (d.conversation_id) return `conversation ${d.conversation_id.slice(0, 8)}`;
  const n = d.candidates?.length ?? 0;
  if (n > 1) return `${n} possible conversations`;
  return "no saved conversation yet";
}

function OutsideRow({ d, onAdopt }: { d: DiscoveredAgent; onAdopt: () => void }) {
  return (
    <div className="flex items-start gap-3 px-4 py-3">
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex min-w-0 items-center gap-2">
          <HarnessBadge harness={d.harness} />
          <Truncate className="min-w-0 font-mono text-[0.7812rem]" title={d.cwd}>
            {displayPath(d.cwd) || "folder unknown"}
          </Truncate>
        </div>
        <p className="tabular text-2xs text-muted-foreground">
          pid {d.pid}
          {d.started_at > 0 && <span title={absoluteTime(d.started_at)}> · started {relativeTime(d.started_at)}</span>} · {conversationLine(d)}
        </p>
        {!d.adoptable && d.reason && <p className="text-2xs text-subtle-foreground">{d.reason}</p>}
      </div>
      <Button size="sm" variant="outline" onClick={onAdopt} disabled={!d.adoptable} title={d.adoptable ? undefined : d.reason}>
        Adopt…
      </Button>
    </div>
  );
}

function AdoptDialog({ d, onClose, onDone }: { d: DiscoveredAgent | null; onClose: () => void; onDone: () => void }) {
  const projects = useStore((s) => s.projects);
  const gateOn = useGateAsks(); // the gate is on and no grace window is open
  const [name, setName] = useState("");
  const [project, setProject] = useState("none");
  const [conv, setConv] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!d) return;
    setName("");
    setProject("none");
    setConv(d.conversation_id || "");
    setBusy(false);
    setError("");
  }, [d]);

  if (!d) return null;
  const base = d.cwd.split(/[\\/]/).filter(Boolean).pop() ?? "";
  const suggested = `${d.harness}-${slugify(base)}`.slice(0, 36).replace(/-+$/, "");
  const candidates = d.candidates ?? [];
  const needsPick = !d.conversation_id && candidates.length > 1;

  async function adopt(e: React.FormEvent) {
    e.preventDefault();
    if (!d || busy || (needsPick && !conv)) return;
    setBusy(true);
    setError("");
    try {
      const a = await api.adopt({
        pid: d.pid,
        name: name.trim() || undefined,
        project_id: project === "none" ? undefined : project,
        conversation_id: conv && conv !== d.conversation_id ? conv : undefined,
        confirm: true,
      });
      toast.success(`Adopted ${a.name}`, { description: a.session_id ? "Its conversation continues under tmx." : "Started under tmx." });
      onDone();
      navigate(paths.agent(a.id));
    } catch (err) {
      setError((err as Error).message || "It wasn't adopted.");
      setBusy(false);
    }
  }

  return (
    <Dialog open={!!d} onOpenChange={(v) => !v && !busy && onClose()}>
      <DialogContent className="sm:max-w-[32.5rem]">
        <form onSubmit={adopt} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>Adopt this session?</DialogTitle>
            <DialogDescription>Its conversation continues as a tmx agent in chat mode.</DialogDescription>
          </DialogHeader>
          <div className="flex items-start gap-2.5 rounded-lg border border-state-waiting/40 bg-state-waiting/[0.07] px-3 py-2.5 text-xs">
            <TriangleAlert className="mt-0.5 size-3.5 shrink-0 text-[color-mix(in_oklch,var(--state-waiting)_80%,var(--foreground))]" />
            <p className="text-foreground/85">
              tmx stops process <span className="tabular font-mono">{d.pid}</span> ({d.harness} in{" "}
              <span className="font-mono">{displayPath(d.cwd)}</span>) with SIGTERM, only that process, and waits for it to exit. Anything
              typed but not sent in that window is lost.
            </p>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid content-start gap-1.5">
              <Label htmlFor="ad-name">Agent name</Label>
              <Input
                id="ad-name"
                value={name}
                onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))}
                placeholder={suggested || "adopted"}
                className="font-mono text-[0.8125rem]"
                autoComplete="off"
                spellCheck={false}
              />
            </div>
            <div className="grid content-start gap-1.5">
              <Label>Project</Label>
              <Select value={project} onValueChange={setProject}>
                <SelectTrigger className="w-full" aria-label="Project">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">None (under the head)</SelectItem>
                  {Object.values(projects)
                    .filter((p) => p.status !== "archived")
                    .map((p) => (
                      <SelectItem key={p.id} value={p.id}>
                        {p.name}
                      </SelectItem>
                    ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          {candidates.length > 1 && (
            <div className="grid gap-1.5">
              <Label>Conversation</Label>
              <Select value={conv} onValueChange={setConv}>
                <SelectTrigger className="w-full" aria-label="Conversation">
                  <SelectValue placeholder="Which conversation is this session on?" />
                </SelectTrigger>
                <SelectContent>
                  {candidates.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      <span className="font-mono text-2xs">{c.id.slice(0, 8)}</span>
                      <span className="max-w-[16.25rem] truncate">{c.preview || "(no message yet)"}</span>
                      <span className="text-2xs text-muted-foreground">{relativeTime(c.updated)}</span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-2xs text-muted-foreground">{d.conversation_source}</p>
            </div>
          )}
          {error && <p className="rounded-md border border-state-error/30 bg-diff-del-bg px-3 py-2 text-xs text-foreground">{error}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy || (needsPick && !conv)} title={gateOn ? "Needs your confirmation on this machine" : undefined}>
              {busy ? <Loader2 className="animate-spin" /> : gateOn && <Fingerprint />}
              Stop and adopt
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function DiscoverSection() {
  const [harnesses, setHarnesses] = useState<HarnessInfo[] | null>(null);
  const [found, setFound] = useState<DiscoveredAgent[] | null>(null);
  const [scanning, setScanning] = useState(false);
  const [error, setError] = useState("");
  const [adopting, setAdopting] = useState<DiscoveredAgent | null>(null);

  const scan = useCallback(async () => {
    setScanning(true);
    setError("");
    try {
      const r = await api.discover();
      setFound(r?.agents ?? []);
    } catch (e) {
      setError((e as Error).message || "The scan failed.");
    } finally {
      setScanning(false);
    }
  }, []);

  useEffect(() => {
    api
      .harnesses()
      .then((h) => setHarnesses(h?.length ? h : FALLBACK_HARNESSES))
      .catch(() => setHarnesses(FALLBACK_HARNESSES));
    void scan();
  }, [scan]);

  const list = found ?? [];
  return (
    <>
      <Section title="Harnesses">
        <div className="divide-y rounded-xl border bg-card">
          {(harnesses ?? FALLBACK_HARNESSES).map((h) => (
            <HarnessRow key={h.name} h={h} />
          ))}
        </div>
      </Section>

      <Section
        title="Running outside tmx"
        right={
          <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={() => void scan()} disabled={scanning}>
            <RefreshCw className={scanning ? "animate-spin" : undefined} />
            Scan again
          </Button>
        }
      >
        <p className="-mt-1 mb-2.5 text-xs text-muted-foreground">
          Claude Code, Codex, opencode and pi sessions started by hand on this machine. Adopting one moves its conversation into tmx.
        </p>
        {error ? (
          <p className="rounded-xl border border-state-error/30 bg-diff-del-bg px-4 py-3 text-sm">{error}</p>
        ) : found === null ? (
          <div className="flex items-center gap-2 rounded-xl border bg-card px-4 py-5 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" /> Scanning this machine…
          </div>
        ) : list.length === 0 ? (
          <div className="flex items-center gap-3 rounded-xl border bg-card px-4 py-5">
            <Radar className="size-4 shrink-0 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">No agent sessions are running outside tmx.</p>
          </div>
        ) : (
          <div className="divide-y rounded-xl border bg-card">
            {list.map((d) => (
              <OutsideRow key={d.pid} d={d} onAdopt={() => setAdopting(d)} />
            ))}
          </div>
        )}
      </Section>
      <AdoptDialog
        d={adopting}
        onClose={() => setAdopting(null)}
        onDone={() => {
          setAdopting(null);
          void scan();
        }}
      />
    </>
  );
}
