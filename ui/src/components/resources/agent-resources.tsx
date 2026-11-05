import { useMemo, useState } from "react";
import { ArrowUpToLine, Hourglass, Pause, Play, ShieldOff, Square } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Meter, Sparkline } from "@/components/resources/sparkline";
import { ordinal } from "@/components/state";
import { api, dropApproval } from "@/lib/api";
import { absoluteTime, relativeTime } from "@/lib/format";
import { bytes, pauseLabel, pct, resourcesApi, useResources } from "@/lib/resources";
import { useStore } from "@/lib/store";
import type { Agent } from "@/lib/types";
import { cn } from "@/lib/utils";

function useBrakeApproval(agentId: string) {
  const approvals = useStore((s) => s.approvals);
  return useMemo(() => Object.values(approvals).find((a) => a.agent_id === agentId && a.kind === "brake" && a.state === "pending"), [approvals, agentId]);
}

/** Resume / Raise limit for an agent a brake paused (through its Needs-you item when there is one). */
export function BrakeActions({ agent, size = "sm" }: { agent: Agent; size?: "sm" | "xs" }) {
  const ap = useBrakeApproval(agent.id);
  const [busy, setBusy] = useState(false);
  const canRaise = !!ap && (agent.paused_reason === "write_budget" || agent.paused_reason === "memory");
  async function go(raise: boolean) {
    if (busy) return;
    setBusy(true);
    try {
      if (ap) {
        await api.decide(ap.id, { decision: raise ? "raise" : "resume" });
        dropApproval(ap.id);
      } else {
        await resourcesApi.resume(agent.id);
      }
      toast(raise ? `Raised the limit and resumed ${agent.name}` : `Resumed ${agent.name}`);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="flex shrink-0 items-center gap-1.5">
      <Button size={size} onClick={() => void go(false)} disabled={busy}>
        <Play className="fill-current" />
        Resume
      </Button>
      {canRaise && (
        <Button size={size} variant="outline" onClick={() => void go(true)} disabled={busy}>
          <ArrowUpToLine />
          Raise limit
        </Button>
      )}
    </div>
  );
}

/** Small numbers for the agent's tab bar: CPU · memory · written today. */
export function AgentUsageInline({ agent }: { agent: Agent }) {
  const u = useResources((s) => s.usage[agent.id]);
  if (!u || (u.procs === 0 && u.written_today === 0)) return null;
  const parts = u.procs > 0 ? [`${pct(u.cpu)} CPU`, bytes(u.mem), `${bytes(u.written_today)} today`] : [`${bytes(u.written_today)} today`];
  return (
    <span className="tabular hidden shrink-0 items-center gap-2 lg:inline-flex" title="CPU, memory and disk writes of the agent's process tree">
      {parts.map((p, i) => (
        <span key={i} className={cn(i > 0 && "before:mr-2 before:text-subtle-foreground before:content-['·']")}>
          {p}
        </span>
      ))}
    </span>
  );
}

/** A strip under the agent header while it waits in the launch queue or is paused by a brake. */
export function AgentResourceBanner({ agent }: { agent: Agent }) {
  const [busy, setBusy] = useState(false);
  if (agent.state === "queued") {
    return (
      <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b bg-muted/30 px-5 py-2.5 md:px-6">
        <Hourglass className="size-4 shrink-0 text-muted-foreground" />
        <p className="min-w-0 flex-1 text-[0.8125rem]">
          <span className="font-medium">Queued{agent.queue_pos ? `, ${ordinal(agent.queue_pos)} in line` : ""}.</span>{" "}
          <span className="text-muted-foreground">
            Waiting for capacity{agent.queue_reason ? ` (${agent.queue_reason})` : ""}; it starts on its own when an agent finishes.
          </span>
        </p>
        <div className="flex items-center gap-1.5">
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                await resourcesApi.startAnyway(agent.id);
                toast(`Starting ${agent.name} now`);
              } catch (e) {
                toast.error((e as Error).message);
              } finally {
                setBusy(false);
              }
            }}
          >
            <Play className="fill-current" />
            Start anyway
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="text-muted-foreground"
            disabled={busy}
            onClick={async () => {
              try {
                await api.agentAction(agent.id, "stop");
                toast(`${agent.name} left the queue`);
              } catch (e) {
                toast.error((e as Error).message);
              }
            }}
          >
            <Square />
            Leave queue
          </Button>
        </div>
      </div>
    );
  }
  if (agent.state !== "paused") return null;
  const head = agent.level === "head";
  return (
    <div role="status" className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b border-state-waiting/30 bg-state-waiting/[0.07] px-5 py-2.5 md:px-6">
      <Pause className="size-4 shrink-0 fill-current text-[color-mix(in_oklch,var(--state-waiting)_80%,var(--foreground))]" />
      <p className="min-w-0 flex-1 text-[0.8125rem]">
        <span className="font-medium">
          Paused by tmx · {pauseLabel(agent)}
          {agent.paused_at ? <span className="tabular font-normal text-muted-foreground"> · {relativeTime(agent.paused_at)}</span> : null}
        </span>
        <span className="block text-xs text-muted-foreground">
          {agent.paused_detail}
          {head ? " The head cannot answer you while it is paused." : " Its whole process tree is stopped; nothing is lost."}
        </span>
      </p>
      <BrakeActions agent={agent} />
    </div>
  );
}

function Stat({ label, value, sub, children }: { label: string; value: string; sub?: string; children?: React.ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col bg-card px-4 pt-3 pb-2.5">
      <dt className="text-2xs font-medium text-subtle-foreground uppercase">{label}</dt>
      <dd className="tabular mt-1 text-[0.9375rem] font-medium">{value}</dd>
      {sub && <dd className="tabular truncate text-2xs text-muted-foreground">{sub}</dd>}
      {children && <dd className="mt-auto pt-2">{children}</dd>}
    </div>
  );
}

/** Details tab: the agent's CPU, memory and writes, with its budget and exemption. */
export function AgentResourceDetails({ agent }: { agent: Agent }) {
  const u = useResources((s) => s.usage[agent.id]);
  const hist = useResources((s) => s.agentHistory[agent.id]);
  const config = useResources((s) => s.config);
  const unavailable = useResources((s) => s.unavailable);
  const cpu = useMemo(() => (hist ?? []).map((p) => p.c), [hist]);
  const mem = useMemo(() => (hist ?? []).map((p) => p.m), [hist]);
  const [busy, setBusy] = useState(false);
  if (unavailable) return null;
  const live = !!u && u.procs > 0;
  const exemptUntil = config?.exempt?.[agent.name] ?? 0;
  const exempt = exemptUntil > Date.now();
  async function setExempt(hours: number) {
    setBusy(true);
    try {
      await resourcesApi.exempt(agent.name, hours);
      toast(hours ? `${agent.name} is exempt from brakes for ${hours} hours` : `Brakes apply to ${agent.name} again`);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="mb-6" aria-label="Resources">
      <div className="mb-2.5 flex h-7 items-center gap-2">
        <h2 className="text-[0.8125rem] font-medium">Resources</h2>
        {live && <span className="tabular text-2xs text-subtle-foreground">{u.procs === 1 ? "1 process" : `${u.procs} processes`}{config?.priority === "normal" ? "" : ", low priority"}</span>}
        <Button
          variant="ghost"
          size="xs"
          className="ml-auto text-muted-foreground"
          disabled={busy}
          onClick={() => void setExempt(exempt ? 0 : 24)}
          title={exempt ? `Exempt until ${absoluteTime(exemptUntil)}` : "Never pause this agent for resources during the next 24 hours"}
        >
          <ShieldOff />
          {exempt ? `Exempt until ${new Date(exemptUntil).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })} · lift` : "Exempt for 24 h"}
        </Button>
      </div>
      <dl className="grid grid-cols-1 gap-px overflow-hidden rounded-xl border bg-border sm:grid-cols-3">
        <Stat label="CPU" value={live ? pct(u.cpu) : "—"} sub={live ? "of one core" : "not running"}>
          <Sparkline values={cpu} max={Math.max(25, ...cpu) * 1.15} />
        </Stat>
        <Stat label="Memory" value={live ? bytes(u.mem) : "—"} sub={config?.memory_cap_mb ? `cap ${bytes(config.memory_cap_mb * 1048576)}` : "no cap"}>
          <Sparkline values={mem} />
        </Stat>
        <Stat
          label="Written today"
          value={bytes(u?.written_today ?? 0)}
          sub={exempt ? "exempt from the budget" : u?.budget ? `of ${bytes(u.budget)} budget` : "no budget"}
        >
          <Meter value={u?.written_today ?? 0} max={exempt ? 0 : (u?.budget ?? 0)} className="mb-1.5" />
        </Stat>
      </dl>
    </section>
  );
}
