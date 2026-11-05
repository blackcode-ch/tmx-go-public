import { useMemo } from "react";
import { Pause } from "lucide-react";
import { BrakeActions } from "@/components/resources/agent-resources";
import { Meter, Sparkline } from "@/components/resources/sparkline";
import { plural } from "@/lib/format";
import { href, paths } from "@/lib/router";
import { bytes, pct, useResources } from "@/lib/resources";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";

function Cell({ label, value, sub, children, className, tone }: {
  label: string;
  value: React.ReactNode;
  sub?: React.ReactNode;
  children?: React.ReactNode;
  className?: string;
  tone?: "attention" | "attention-sub";
}) {
  return (
    <div className={cn("flex min-w-0 flex-col bg-card px-4 pt-3 pb-2.5", className)}>
      <dt className="text-2xs font-medium text-subtle-foreground uppercase">{label}</dt>
      <dd className={cn("tabular mt-1 truncate text-[0.9375rem] font-medium", tone === "attention" && "text-[color-mix(in_oklch,var(--state-waiting)_75%,var(--foreground))]")}>{value}</dd>
      {sub && <dd className={cn("tabular truncate text-2xs text-muted-foreground", tone && "text-[color-mix(in_oklch,var(--state-waiting)_75%,var(--foreground))]")}>{sub}</dd>}
      {children && <dd className="mt-auto pt-2">{children}</dd>}
    </div>
  );
}

/** Overview: the machine at a glance (CPU, memory, disk, agents, slots). */
export function MachineStrip() {
  const sample = useResources((s) => s.sample);
  const hist = useResources((s) => s.machineHistory);
  const slots = useResources((s) => s.slots);
  const unavailable = useResources((s) => s.unavailable);
  const cpu = useMemo(() => hist.map((p) => p.c), [hist]);
  const mem = useMemo(() => hist.map((p) => p.m), [hist]);
  if (unavailable) return null;
  if (!sample) {
    return <div className="h-[6.5rem] rounded-xl border bg-card" aria-busy="true" />;
  }
  const m = sample.machine;
  const used = m.disk_total > 0 ? m.disk_total - m.disk_free : 0;
  const inUse = slots.kinds.reduce((n, k) => n + k.held, 0);
  const size = slots.kinds.reduce((n, k) => n + k.size, 0);
  const waiting = slots.kinds.reduce((n, k) => n + k.waiting, 0);
  const others = [m.queued > 0 && `${m.queued} queued`, m.paused > 0 && `${m.paused} paused`].filter(Boolean).join(" · ");
  return (
    <section aria-label="This machine">
      <dl className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border sm:grid-cols-3 lg:grid-cols-5">
        <Cell label="CPU" value={pct(m.cpu)} sub={m.cores ? plural(m.cores, "core") : undefined}>
          <Sparkline values={cpu} max={100} label="CPU over the last minutes" />
        </Cell>
        <Cell label="Memory" value={`${bytes(m.mem_free)} free`} sub={m.mem_total ? `of ${bytes(m.mem_total)}` : undefined}>
          <Sparkline values={mem} max={m.mem_total || undefined} label="Free memory over the last minutes" />
        </Cell>
        <Cell
          label="Disk"
          value={`${bytes(m.disk_free)} free`}
          tone={m.saturated ? "attention" : undefined}
          sub={m.saturated ? "Saturated: the heaviest writer takes a turn" : m.disk_write_rate > 0 ? `writing ${bytes(m.disk_write_rate)}/s` : m.disk_total ? `of ${bytes(m.disk_total)}` : undefined}
        >
          <Meter value={used} max={m.disk_total} className="mb-1.5" />
        </Cell>
        <Cell label="Agents" value={`${m.running} running`} sub={others || "none waiting"} tone={m.paused > 0 ? "attention-sub" : undefined}>
          <span className="text-2xs text-subtle-foreground tabular">{bytes(m.written_today)} written today</span>
        </Cell>
        <Cell
          label="Slots"
          className="col-span-2 sm:col-span-1"
          value={size ? `${inUse} of ${size} in use` : "None"}
          sub={waiting ? `${waiting} waiting` : slots.kinds.map((k) => `${k.kind} ${k.held}/${k.size}`).join(" · ")}
        >
          <span className="flex gap-2.5">
            {slots.kinds.map((k) => (
              <span key={k.kind} className="flex flex-1 gap-1" title={`${k.kind}: ${k.held} of ${k.size} in use`}>
                {Array.from({ length: k.size }, (_, i) => (
                  <span key={i} className={cn("h-1.5 flex-1 rounded-full", i < k.held ? "bg-brand/70" : "bg-muted")} />
                ))}
              </span>
            ))}
          </span>
        </Cell>
      </dl>
    </section>
  );
}

/** Overview: a paused head cannot answer the owner, so it is said loudly. */
export function HeadPausedBanner() {
  const head = useStore((s) => (s.headId ? s.agents[s.headId] : undefined));
  if (!head || head.state !== "paused") return null;
  return (
    <section role="alert" className="overflow-hidden rounded-xl border border-state-waiting/50 bg-card">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 bg-state-waiting/[0.08] px-4 py-3">
        <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-state-waiting/20">
          <Pause className="size-3.5 fill-current text-[color-mix(in_oklch,var(--state-waiting)_80%,var(--foreground))]" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="text-[0.8125rem] font-semibold">The head is paused by tmx</p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {head.paused_detail || "A resource brake stopped it."} It cannot answer you or run projects until it is resumed.{" "}
            <a className="text-brand hover:underline" href={href(paths.agent(head.id))}>
              Open the head
            </a>
          </p>
        </div>
        <BrakeActions agent={head} />
      </div>
    </section>
  );
}
