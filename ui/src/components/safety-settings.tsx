// Settings → Safety: the dangerous-command guard (on/off, per level), its rules, and the owner
// gate with its grace window (DESIGN.md "Safety"). Changes that protect less go through the owner gate.
import { useEffect, useState } from "react";
import { AlertTriangle, Ban, ChevronRight, Fingerprint, KeyRound, LifeBuoy, Lock, LockOpen, ShieldAlert, ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Skeleton } from "@/components/ui/skeleton";
import { Slider } from "@/components/ui/slider";
import { Switch } from "@/components/ui/switch";
import { CopyButton } from "@/components/copy-button";
import { PinInput, validPin } from "@/components/pin-input";
import { Section } from "@/components/page";
import { api } from "@/lib/api";
import { displayPath, LEVEL_LABEL } from "@/lib/format";
import { GRACE_STEPS, graceLabel, graceTick, graceUntilLabel, loadSafety, setGateStatus, useGate, useGraceUntil } from "@/lib/owner-gate";
import type { GateStatus, GuardAlwaysAllow, GuardRule, GuardSettings, Level, PinStatus, RuleState } from "@/lib/types";
import { cn } from "@/lib/utils";

const LEVELS: Level[] = ["head", "lead", "worker"];

function Card({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("rounded-xl border bg-card", className)}>{children}</div>;
}

function ToggleRow({
  id,
  label,
  hint,
  checked,
  disabled,
  onChange,
}: {
  id: string;
  label: React.ReactNode;
  hint?: React.ReactNode;
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-start gap-4 px-4 py-3">
      <label htmlFor={id} className="min-w-0 flex-1 cursor-pointer">
        <div className="text-sm">{label}</div>
        {hint && <div className="mt-0.5 text-xs text-muted-foreground">{hint}</div>}
      </label>
      <Switch id={id} checked={checked} disabled={disabled} onCheckedChange={onChange} className="mt-0.5" />
    </div>
  );
}

function GuardCard({ g }: { g: GuardSettings }) {
  const [busy, setBusy] = useState(false);

  async function save(next: GuardSettings, done: string) {
    setBusy(true);
    try {
      const saved = await api.putGuard(next);
      useGate.setState((s) => (s.safety ? { safety: { ...s.safety, guard: saved ?? next } } : {}));
      toast.success(done, { description: "Running agents keep their current setting until they restart." });
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const offFor = new Set(g.off_for ?? []);
  return (
    <Card className="divide-y">
      <ToggleRow
        id="guard-on"
        label={
          <span className="flex items-center gap-1.5 font-medium">
            {g.enabled ? <ShieldCheck className="size-4 text-state-ok" /> : <ShieldAlert className="size-4 text-state-error" />}
            Dangerous-command guard
          </span>
        }
        hint="Checks every command and file write of chat-mode Claude agents, in every permission mode, including Bypass permissions."
        checked={g.enabled}
        disabled={busy}
        onChange={(on) => void save({ ...g, enabled: on }, on ? "Guard switched on" : "Guard switched off")}
      />
      {!g.enabled && (
        <div className="flex items-start gap-2 bg-diff-del-bg px-4 py-2.5 text-xs">
          <AlertTriangle className="mt-px size-3.5 shrink-0 text-state-error" />
          <span>Nothing stops an agent from wiping a disk, deleting your home folder or killing other agents' tools.</span>
        </div>
      )}
      {LEVELS.map((l) => (
        <ToggleRow
          key={l}
          id={"guard-" + l}
          label={`${LEVEL_LABEL[l]} agents`}
          checked={g.enabled && !offFor.has(l)}
          disabled={busy || !g.enabled}
          onChange={(on) => {
            const next = new Set(offFor);
            if (on) next.delete(l);
            else next.add(l);
            void save({ ...g, off_for: [...next] }, on ? `Guard on for ${l}s` : `Guard off for ${l}s`);
          }}
        />
      ))}
      <p className="px-4 py-2.5 text-xs text-muted-foreground">
        Terminal-mode agents are not covered: the guard cannot see what is typed into a terminal.
      </p>
    </Card>
  );
}

const STATE_LABEL: Record<RuleState, string> = { block: "Blocked", hold: "Held for me", allow: "Allowed" };

/**
 * A rule's switch (the owner, 3 Nov): three ways on the Blocked list, Held for me / Allowed on the
 * Held list. Each flip asks for your PIN (or Windows Hello at the machine) and is logged.
 */
function RuleSwitch({ r, onSaved }: { r: GuardRule; onSaved: (g: GuardSettings, rules: GuardRule[]) => void }) {
  const [busy, setBusy] = useState(false);
  const states: RuleState[] = r.outcome === "block" ? ["block", "hold", "allow"] : ["hold", "allow"];
  const cur = r.state ?? r.default ?? r.outcome;
  async function flip(next: RuleState) {
    if (next === cur || busy) return;
    setBusy(true);
    try {
      const out = await api.putRuleState(r.id, next);
      if (out) onSaved(out.guard, out.rules);
      toast.success(`${r.name}: ${STATE_LABEL[next]}`);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div role="radiogroup" aria-label={`${r.name}: what agents get`} className="inline-flex shrink-0 overflow-hidden rounded-md border text-2xs" onClick={(e) => e.stopPropagation()}>
      {states.map((st) => (
        <button
          key={st}
          type="button"
          role="radio"
          aria-checked={cur === st}
          disabled={busy}
          onClick={() => void flip(st)}
          className={cn(
            "px-2 py-1 font-medium transition-colors not-first:border-l disabled:opacity-60",
            cur === st
              ? st === "block"
                ? "bg-state-error/15 text-foreground"
                : st === "hold"
                  ? "bg-state-waiting/20 text-foreground"
                  : "bg-state-ok/15 text-foreground"
              : "text-muted-foreground hover:bg-accent/50",
          )}
        >
          {STATE_LABEL[st]}
        </button>
      ))}
    </div>
  );
}

function RuleRow({ r, onSaved }: { r: GuardRule; onSaved: (g: GuardSettings, rules: GuardRule[]) => void }) {
  const [open, setOpen] = useState(false);
  const cur = r.state ?? r.outcome;
  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <div className="flex w-full items-start gap-3 px-4 py-2.5">
        <CollapsibleTrigger className="flex min-w-0 flex-1 items-start gap-3 text-left outline-none hover:opacity-90 focus-visible:underline">
          {cur === "block" ? (
            <Ban className="mt-0.5 size-3.5 shrink-0 text-state-error" />
          ) : cur === "hold" ? (
            <ShieldAlert className="mt-0.5 size-3.5 shrink-0 text-[color-mix(in_oklch,var(--state-waiting)_80%,var(--foreground))]" />
          ) : (
            <ShieldCheck className="mt-0.5 size-3.5 shrink-0 text-state-ok" />
          )}
          <span className="min-w-0 flex-1">
            <span className="block text-[0.8125rem] font-medium">{r.name}</span>
            <span className="block text-xs text-muted-foreground">{r.summary}</span>
            {r.always_on && <span className="mt-0.5 block text-2xs text-foreground/75">Always on: {r.always_on}</span>}
          </span>
          <ChevronRight className={cn("mt-0.5 size-3.5 shrink-0 text-subtle-foreground transition-transform duration-150", open && "rotate-90")} />
        </CollapsibleTrigger>
        {r.switchable !== false && r.state && <RuleSwitch r={r} onSaved={onSaved} />}
      </div>
      <CollapsibleContent>
        <div className="space-y-2 px-4 pb-3 pl-10.5">
          <p className="text-xs leading-relaxed text-foreground/85">{r.why}</p>
          {r.examples?.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {r.examples.map((x) => (
                <code key={x} className="rounded-md border bg-code px-1.5 py-0.5 font-mono text-[0.7188rem] text-foreground/80">
                  {x}
                </code>
              ))}
            </div>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

/** The standing "Allow always" answers: what runs without asking, for whom; Revoke asks for a proof. */
function AlwaysCard({ items }: { items: GuardAlwaysAllow[] }) {
  const [busy, setBusy] = useState("");
  async function revoke(al: GuardAlwaysAllow) {
    setBusy(al.id);
    try {
      const saved = await api.revokeAlways(al.id);
      useGate.setState((s) => (s.safety && saved ? { safety: { ...s.safety, guard: saved } } : {}));
      toast.success("Revoked; it asks you again");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy("");
    }
  }
  if (!items.length) {
    return <Card className="px-4 py-3 text-xs text-muted-foreground">None. Answer a held command with Allow always to stop being asked for exactly that command.</Card>;
  }
  return (
    <Card className="divide-y">
      {items.map((al) => (
        <div key={al.id} className="flex items-start gap-3 px-4 py-3">
          <div className="min-w-0 flex-1">
            <pre className="font-mono text-[0.75rem] whitespace-pre-wrap text-foreground/90 [overflow-wrap:anywhere]">{al.command}</pre>
            <p className="mt-1 text-xs text-muted-foreground">
              {al.rule_name} · {al.tool} · {al.profile ? `agents like ${al.from || al.profile} (${al.profile})` : "any agent"}
              {al.cwd ? ` · in ${displayPath(al.cwd)}` : ""}
            </p>
          </div>
          <Button size="sm" variant="outline" disabled={busy !== ""} onClick={() => void revoke(al)} title="Needs your confirmation on this machine">
            <Fingerprint />
            Revoke
          </Button>
        </div>
      ))}
    </Card>
  );
}

function RulesCard({ rules, log }: { rules: GuardRule[]; log?: GuardSettings["rule_log"] }) {
  const onSaved = (g: GuardSettings, next: GuardRule[]) =>
    useGate.setState((s) => (s.safety ? { safety: { ...s.safety, guard: g, rules: next } } : {}));
  // Fixed carve-outs have no switch: their parent row says "Always on: ...".
  const shown = rules.filter((r) => r.switchable !== false);
  const groups: { title: string; hint: string; items: GuardRule[] }[] = [
    { title: "Blocked", hint: "Blocked never runs; Held waits for you; Allowed runs. Every change asks for your PIN.", items: shown.filter((r) => r.outcome === "block") },
    { title: "Held for you", hint: "Held waits in Needs you (Allow once, Allow always or Deny; no answer in 10 minutes means no); Allowed runs.", items: shown.filter((r) => r.outcome === "hold") },
  ];
  return (
    <div className="space-y-3">
      {groups.map((gr) => (
        <Card key={gr.title} className="overflow-hidden">
          <div className="flex items-baseline gap-2 border-b px-4 py-2.5">
            <h3 className="text-[0.8125rem] font-semibold">{gr.title}</h3>
            <p className="text-xs text-muted-foreground">{gr.hint}</p>
          </div>
          <div className="divide-y">
            {gr.items.map((r) => (
              <RuleRow key={r.id} r={r} onSaved={onSaved} />
            ))}
          </div>
        </Card>
      ))}
      <p className="text-xs text-muted-foreground">Everything else runs as usual; the agent's permission mode still applies.</p>
      {log && log.length > 0 && (
        <Card className="overflow-hidden">
          <div className="border-b px-4 py-2.5 text-[0.8125rem] font-semibold">Changes</div>
          <ul className="max-h-56 divide-y overflow-auto text-xs">
            {[...log].reverse().slice(0, 50).map((ch, i) => (
              <li key={i} className="flex items-baseline gap-2 px-4 py-1.5">
                <span className="min-w-0 flex-1 truncate">{rules.find((r) => r.id === ch.rule)?.name ?? ch.rule}</span>
                <span className="text-muted-foreground">
                  {STATE_LABEL[ch.old] ?? ch.old} → <span className="text-foreground">{STATE_LABEL[ch.new] ?? ch.new}</span>
                </span>
                <span className="tabular w-36 shrink-0 text-right text-2xs text-subtle-foreground">
                  {ch.by === "tmx" ? "tmx defaults · " : ""}
                  {new Date(ch.at).toLocaleString([], { dateStyle: "short", timeStyle: "short" })}
                </span>
              </li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}

/**
 * The grace window: after a proof the daemon stops asking for this long (a dial of fixed steps).
 * Longer asks for a fresh proof (the sheet then names the new length); shorter, Off and Lock now
 * never do. The window lives in the daemon's memory, so a restart locks it.
 */
function GraceRow({ gate }: { gate: GateStatus }) {
  const steps = gate.grace_steps?.length ? gate.grace_steps : GRACE_STEPS;
  const minutes = gate.grace_minutes ?? 0;
  const until = useGraceUntil();
  const [draft, setDraft] = useState<number | null>(null); // the step being dragged or saved
  const [busy, setBusy] = useState(false);
  const at = steps.indexOf(minutes);
  const idx = draft ?? (at >= 0 ? at : 0);
  const shown = steps[idx] ?? 0;
  const disabled = busy || !gate.available;

  async function save(i: number) {
    const next = steps[i];
    if (next === undefined || next === minutes) {
      setDraft(null);
      return;
    }
    setDraft(i);
    setBusy(true);
    if (next > minutes) useGate.setState({ graceAsked: next });
    try {
      const st = await api.putGrace(next);
      if (st) setGateStatus(st);
      toast.success(next === 0 ? "tmx asks every time" : `No prompt for ${graceLabel(next)} after a confirmation`);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
      setDraft(null);
      useGate.setState({ graceAsked: null });
    }
  }

  async function lock() {
    setBusy(true);
    try {
      setGateStatus(await api.lockGate());
      toast.success("Locked: the next action asks again");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="px-4 py-3" data-testid="grace">
      <div className="flex items-baseline gap-2">
        <div className="text-sm">Don't ask again for</div>
        <div className="ml-auto text-sm font-medium tabular-nums">{graceLabel(shown)}</div>
      </div>
      <p className="mt-0.5 text-xs text-muted-foreground">
        After you confirm once, tmx skips the prompt for this long. You still decide each action.
      </p>
      <div className="mt-3.5">
        <Slider
          min={0}
          max={steps.length - 1}
          step={1}
          value={[idx]}
          disabled={disabled}
          onValueChange={([i]) => setDraft(i)}
          onValueCommit={([i]) => void save(i)}
          thumbLabel="Don't ask again for"
          valueText={graceLabel(shown)}
        />
        <div className="mt-1.5 flex h-4 justify-between px-2">
          {steps.map((m, i) => (
            <span key={m} className="relative w-0">
              <button
                type="button"
                tabIndex={-1}
                disabled={disabled}
                onClick={() => void save(i)}
                className={cn(
                  "absolute left-1/2 -translate-x-1/2 px-0.5 text-2xs whitespace-nowrap text-muted-foreground tabular-nums outline-none hover:text-foreground disabled:pointer-events-none",
                  i === idx && "font-medium text-foreground",
                )}
              >
                {graceTick(m)}
              </button>
            </span>
          ))}
        </div>
      </div>
      <div className="mt-3 flex min-h-9 flex-wrap items-center gap-x-2 gap-y-1.5 rounded-lg border bg-muted/40 px-3 py-1.5 text-xs">
        {until ? (
          <>
            <LockOpen className="size-3.5 shrink-0 text-brand" />
            <span>
              No confirmation needed until <span className="font-medium tabular-nums">{graceUntilLabel(until)}</span>
            </span>
            <Button size="xs" variant="outline" className="ml-auto" disabled={busy} onClick={() => void lock()}>
              <Lock />
              Lock now
            </Button>
          </>
        ) : (
          <>
            <Lock className="size-3.5 shrink-0 text-subtle-foreground" />
            <span className="text-muted-foreground">{minutes === 0 ? "Asks every time." : "Locked: the next action asks."}</span>
          </>
        )}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        {gate.available ? "" : "Needs a way to confirm on this machine. "}
        Kept in memory only: a restart or update locks it. Only this UI session can use its window. Turning the gate on or off, and a
        longer window, always ask.
      </p>
    </div>
  );
}

/**
 * Windows Hello / Touch ID at this machine: an optional shortcut beside the owner PIN, which
 * works on every device (#187). Off is refused while no PIN is set: it would be the last way in.
 */
function HelloRow({ gate }: { gate: GateStatus }) {
  const [busy, setBusy] = useState(false);
  const on = !!gate.hello_shortcut;
  async function toggle(next: boolean) {
    setBusy(true);
    try {
      const st = await api.putHello(next);
      if (st) setGateStatus(st);
      toast.success(next ? "Windows Hello is offered too" : "Only your owner PIN now");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <ToggleRow
      id="gate-hello"
      label="Also accept Windows Hello on this machine"
      hint={
        gate.pin_set
          ? "Your owner PIN always works, here and on your paired devices. This only adds a shortcut at this machine."
          : "Set your owner PIN first: until then Windows Hello is the only way to confirm here."
      }
      checked={on}
      disabled={busy || (on && !gate.pin_set)}
      onChange={(v) => void toggle(v)}
    />
  );
}

function GateCard() {
  const gate = useGate((s) => s.safety?.gate);
  const [busy, setBusy] = useState(false);
  if (!gate) return null;
  const remote = gate.method === "pin";

  async function toggle(on: boolean) {
    setBusy(true);
    try {
      const st = await api.putGate(on);
      if (st) setGateStatus(st);
      toast.success(on ? "Owner gate on" : "Owner gate off");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="divide-y">
      <div className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-start">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5 text-sm font-medium">
            <Fingerprint className={cn("size-4", gate.enabled ? "text-brand" : "text-subtle-foreground")} />
            Owner gate
            <span
              className={cn(
                "ml-1 rounded-md border px-1.5 py-px text-2xs font-medium",
                gate.enabled ? "border-brand/30 bg-brand/10 text-foreground" : "text-muted-foreground",
              )}
            >
              {gate.enabled ? "On" : "Off"}
            </span>
          </div>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {remote ? "Owner actions ask for your owner PIN on this device. Change the gate switch and grace settings on the host computer." : gate.available
              ? `The daemon asks for ${gate.method_label} on this machine before the actions below. Once enrolled, missing state keeps it on; turning it on or off asks once too.`
              : `Not available here: ${gate.unavailable || "no way to ask the owner on this machine"}.`}
          </p>
        </div>
        <Button
          size="sm"
          variant={gate.enabled ? "outline" : "default"}
          disabled={remote || busy || (!gate.enabled && !gate.available)}
          onClick={() => void toggle(!gate.enabled)}
          className="shrink-0"
        >
          <Fingerprint />
          {gate.enabled ? "Turn off" : "Turn on"}
        </Button>
      </div>
      {!remote && gate.grace_steps && <GraceRow gate={gate} />}
      {!remote && gate.pin_set !== undefined && <HelloRow gate={gate} />}
      <div className="px-4 py-3">
        <div className="mb-1.5 text-xs font-medium text-muted-foreground">{gate.enabled ? "Asks before" : "Will ask before"}</div>
        <ul className="grid gap-1 text-[0.8125rem] sm:grid-cols-2">
          {gate.gated.map((x) => (
            <li key={x} className="flex items-start gap-1.5">
              <span className="mt-[0.4375rem] size-1 shrink-0 rounded-full bg-subtle-foreground" />
              {x}
            </li>
          ))}
        </ul>
      </div>
      <div className="flex items-start gap-2.5 px-4 py-3 text-xs text-muted-foreground">
        <LifeBuoy className="mt-px size-3.5 shrink-0" />
        <div className="min-w-0 space-y-1">
          <p>
            {gate.recovery || "The gate protects tmx actions, never your computer login or files. See the documented offline recovery procedure if confirmation stops working."}
          </p>
          {gate.file && (
            <div className="flex min-w-0 items-center gap-1">
              <code className="truncate font-mono text-[0.7188rem] text-foreground/85" title={gate.file}>
                {displayPath(gate.file)}
              </code>
              <CopyButton text={gate.file} label="Copy path" />
            </div>
          )}
        </div>
      </div>
    </Card>
  );
}

// The owner PIN: ONE short code (6 to 12 digits) for every owner step from a phone or another
// browser, the same on every machine and in the Python tmx. Set it here at the machine; from
// a browser it changes only with the current one. Wrong tries slow down, never lock out.
function PinCard() {
  const [st, setSt] = useState<PinStatus | null>(null);
  const [pin, setPin] = useState("");
  const [again, setAgain] = useState("");
  const [old, setOld] = useState("");
  const [busy, setBusy] = useState(false);
  const load = () => api.pinStatus().then(setSt, () => setSt(null));
  useEffect(() => {
    void load();
  }, []);
  if (!st?.available) return null;
  const here = !!st.at_machine;
  const needOld = st.set && !here;
  const ok = validPin(pin) && pin === again && (!needOld || validPin(old));

  async function save() {
    setBusy(true);
    try {
      await api.setPin(pin, needOld ? old : undefined);
      toast.success(st?.set ? "Owner PIN changed on this machine (the others pick it up within 5 min)" : "Owner PIN set");
      setPin("");
      setAgain("");
      setOld("");
      await load();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function clearWaits() {
    try {
      await api.clearPinWaits();
      toast.success("Wrong-PIN waits cleared");
      await load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <Card className="divide-y">
      <div className="px-4 py-3">
        <div className="flex items-center gap-1.5 text-sm font-medium">
          <KeyRound className={cn("size-4", st.set ? "text-brand" : "text-subtle-foreground")} />
          Owner PIN
          <span className="ml-1 rounded-md border px-1.5 py-px text-2xs font-medium text-muted-foreground">{st.set ? "Set" : "Not set"}</span>
        </div>
        <p className="mt-0.5 text-xs text-muted-foreground">
          One short code for approvals and pairing from your phone or another browser: the same on all your machines. Wrong tries make you wait a little, never lock you out.
          {!here && !st.set && " Set it on the machine itself."}
          {!here && st.set && " Lost it? Set a new one on the machine itself."}
        </p>
        {st.error && <p className="mt-1 text-xs text-destructive">{st.error}</p>}
        {(here || st.set) && (
          <form
            className="mt-3 grid max-w-xs gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (ok) void save();
            }}
          >
            {needOld && <PinInput label="Current PIN" value={old} onChange={setOld} />}
            <PinInput label={st.set ? "New PIN" : "PIN"} value={pin} onChange={setPin} />
            <PinInput label="The same PIN again" value={again} onChange={setAgain} />
            {again && pin !== again && <p className="text-xs text-destructive">The two PINs differ.</p>}
            <div className="flex gap-2">
              <Button type="submit" size="sm" disabled={!ok || busy}>
                {st.set ? "Change PIN" : "Set PIN"}
              </Button>
              {here && st.set && (
                <Button type="button" size="sm" variant="outline" onClick={() => void clearWaits()}>
                  Clear wrong-PIN waits
                </Button>
              )}
            </div>
          </form>
        )}
      </div>
    </Card>
  );
}

export function SafetySection() {
  const safety = useGate((s) => s.safety);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let live = true;
    void loadSafety().then((s) => live && setFailed(!s));
    return () => {
      live = false;
    };
  }, []);

  return (
    <Section title="Safety">
      <p className="-mt-1 mb-3 text-xs text-muted-foreground">What agents may never do, what waits for you, and how tmx makes sure it is you.</p>
      {!safety ? (
        failed ? (
          <Card className="px-4 py-3 text-sm text-muted-foreground">This daemon doesn't report its safety settings. Update tmx.</Card>
        ) : (
          <div className="space-y-3">
            <Skeleton className="h-40 rounded-xl" />
            <Skeleton className="h-24 rounded-xl" />
          </div>
        )
      ) : (
        <div className="space-y-6">
          <GuardCard g={safety.guard} />
          <div>
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">Allowed always</h3>
            <AlwaysCard items={safety.guard.always_allow ?? []} />
          </div>
          <div>
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">Rules</h3>
            <RulesCard rules={safety.rules} log={safety.guard.rule_log} />
          </div>
          <div>
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">Confirmation</h3>
            <GateCard />
          </div>
          <div>
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">Owner PIN</h3>
            <PinCard />
          </div>
        </div>
      )}
    </Section>
  );
}
