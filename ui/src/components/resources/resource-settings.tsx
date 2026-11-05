import { useEffect, useMemo, useState } from "react";
import { Loader2, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Section } from "@/components/page";
import { absoluteTime } from "@/lib/format";
import { resourcesApi, useResources, type ResourceConfig } from "@/lib/resources";
import { cn } from "@/lib/utils";

function Row({ label, hint, children, htmlFor }: { label: string; hint?: React.ReactNode; children: React.ReactNode; htmlFor?: string }) {
  return (
    <div className="flex flex-col gap-2 py-3 sm:flex-row sm:items-start sm:gap-6">
      <div className="min-w-0 sm:w-56 sm:shrink-0 sm:pt-1.5">
        <label htmlFor={htmlFor} className="text-sm">
          {label}
        </label>
        {hint && <div className="mt-0.5 text-xs text-muted-foreground">{hint}</div>}
      </div>
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2 sm:pt-0.5">{children}</div>
    </div>
  );
}

/** A number field with a unit; empty means "off" where off is allowed. */
function NumberField({
  id,
  value,
  onChange,
  unit,
  off,
  step = 1,
  min = 0,
}: {
  id: string;
  value: number;
  onChange: (v: number) => void;
  unit: string;
  off?: string;
  step?: number;
  min?: number;
}) {
  const shown = off && value <= 0 ? "" : String(value);
  return (
    <span className="inline-flex items-center gap-2">
      <Input
        id={id}
        type="number"
        inputMode="decimal"
        min={min}
        step={step}
        value={shown}
        placeholder={off}
        onChange={(e) => {
          const t = e.target.value.trim();
          if (t === "") onChange(off ? -1 : 0);
          else if (!Number.isNaN(Number(t))) onChange(Number(t));
        }}
        className="tabular h-8 w-28 text-right"
      />
      <span className="text-xs text-muted-foreground">{unit}</span>
    </span>
  );
}

const SLOT_HINT: Record<string, string> = {
  heavy: "Builds, test suites, installs, big clones.",
  browser: "Anything that starts a browser.",
};

/** Settings → Resources: the launch queue, priority, brakes and slots (config.json "resources"). */
export function ResourceSettings() {
  const config = useResources((s) => s.config);
  const cost = useResources((s) => s.cost);
  const support = useResources((s) => s.support);
  const unavailable = useResources((s) => s.unavailable);
  const [draft, setDraft] = useState<ResourceConfig | null>(config);
  const [saving, setSaving] = useState(false);
  useEffect(() => setDraft(config), [config]);
  const dirty = useMemo(() => !!draft && JSON.stringify(draft) !== JSON.stringify(config), [draft, config]);
  if (unavailable) return null;
  if (!draft) return <div id="resources" className="h-64 rounded-xl border bg-card" aria-busy="true" />;

  const up = (p: Partial<ResourceConfig>) => setDraft((d) => (d ? { ...d, ...p } : d));
  const exempt = Object.entries(draft.exempt ?? {}).filter(([, until]) => until > Date.now());

  async function save() {
    if (!draft) return;
    setSaving(true);
    try {
      await resourcesApi.putConfig(draft);
      toast.success("Resource limits saved", { description: "They apply from the next start and the next sample." });
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Section
      title={<span id="resources">Resources</span>}
      right={
        dirty && (
          <>
            <Button variant="ghost" size="xs" onClick={() => setDraft(config)} disabled={saving}>
              Discard
            </Button>
            <Button size="xs" onClick={() => void save()} disabled={saving}>
              {saving && <Loader2 className="animate-spin" />}
              Save limits
            </Button>
          </>
        )
      }
    >
      <p className="-mt-1 mb-3 text-xs text-muted-foreground">
        How agents share this machine. Nothing is ever killed: starts wait in line, and an agent over a limit is paused until you
        decide.
      </p>
      <div className="space-y-3">
        <div className="divide-y rounded-xl border bg-card px-4">
          <Row label="Running agents" htmlFor="res-max" hint="The head counts too. More starts wait in line and begin on their own.">
            <NumberField id="res-max" value={draft.max_agents} onChange={(v) => up({ max_agents: v })} unit="at most" off="No limit" />
          </Row>
          <Row label="Free memory" htmlFor="res-ram" hint="New agents wait while less than this is free.">
            <NumberField id="res-ram" value={draft.min_free_ram_mb} onChange={(v) => up({ min_free_ram_mb: v })} unit="MB at least" off="Off" step={100} />
          </Row>
          <Row label="Low priority" htmlFor="res-prio" hint="Agents yield CPU and disk to you: nice 10 and a low I/O priority on macOS and Linux, below normal on Windows. Applies to agents started from now on.">
            <Switch id="res-prio" checked={draft.priority !== "normal"} onCheckedChange={(v) => up({ priority: v ? "low" : "normal" })} />
          </Row>
        </div>

        <div className="divide-y rounded-xl border bg-card px-4">
          <Row label="Daily write budget" htmlFor="res-budget" hint="Per agent and day. Over it, its process tree is paused and Needs you asks.">
            <NumberField id="res-budget" value={draft.daily_write_gb} onChange={(v) => up({ daily_write_gb: v })} unit="GB per agent" off="Off" step={1} />
          </Row>
          <Row label="Memory cap" htmlFor="res-mem" hint="Per agent tree. Over it, the agent is paused.">
            <NumberField id="res-mem" value={draft.memory_cap_mb} onChange={(v) => up({ memory_cap_mb: v })} unit="MB per agent" off="Off" step={256} />
          </Row>
          <Row
            label="Saturated disk"
            htmlFor="res-disk"
            hint={support && !support.disk_queue ? "This system does not report its disk queue; the brake cannot trigger here." : "The agent writing the most takes a short pause while the disk is overloaded."}
          >
            <Switch id="res-disk" checked={draft.disk_queue > 0} onCheckedChange={(v) => up({ disk_queue: v ? 8 : -1 })} />
            {draft.disk_queue > 0 && (
              <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
                pause for
                <NumberField id="res-turn" value={draft.saturation_pause_secs} onChange={(v) => up({ saturation_pause_secs: v })} unit="seconds" step={10} />
              </span>
            )}
          </Row>
          <Row label="Exempt agents" hint="Never paused until the time shown.">
            {exempt.length === 0 ? (
              <p className="pt-1 text-sm text-muted-foreground">None. Exempt an agent from its Details tab.</p>
            ) : (
              <ul className="flex w-full flex-col gap-1">
                {exempt.map(([name, until]) => (
                  <li key={name} className="flex h-7 items-center gap-2 text-sm">
                    <span className="font-medium">{name}</span>
                    <span className="tabular text-xs text-muted-foreground">until {absoluteTime(until)}</span>
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      className="ml-auto text-muted-foreground"
                      aria-label={`Lift the exemption of ${name}`}
                      onClick={async () => {
                        try {
                          await resourcesApi.exempt(name, 0);
                        } catch (e) {
                          toast.error((e as Error).message);
                        }
                      }}
                    >
                      <X />
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </Row>
        </div>

        <div className="divide-y rounded-xl border bg-card px-4">
          {Object.keys(draft.slots)
            .sort()
            .map((k) => (
              <Row key={k} label={`${k[0].toUpperCase()}${k.slice(1)} slots`} htmlFor={`res-slot-${k}`} hint={SLOT_HINT[k]}>
                <NumberField
                  id={`res-slot-${k}`}
                  value={draft.slots[k]}
                  min={1}
                  onChange={(v) => up({ slots: { ...draft.slots, [k]: Math.max(1, Math.round(v)) } })}
                  unit="at once"
                />
              </Row>
            ))}
          <p className="py-3 text-xs text-muted-foreground">
            Agents run heavy commands through <code className="rounded border bg-muted px-1 font-mono text-[0.6875rem]">tmx slot -- …</code>, which
            waits for a free slot of its kind.
          </p>
        </div>
        <p className={cn("px-1 text-2xs text-subtle-foreground tabular")}>
          Measured every {draft.sample_secs} s
          {cost && cost.ticks > 0 ? `; each sample takes ${cost.avg_ms.toFixed(1)} ms (${cost.cpu_pct < 0.01 ? "<0.01" : cost.cpu_pct.toFixed(2)}% of one core)` : ""}.
          {support && !support.per_process ? " This build cannot measure per agent." : ""}
        </p>
      </div>
    </Section>
  );
}
