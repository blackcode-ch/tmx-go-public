// One AI account on the Usage page: who is signed in, its quotas, and Connect / Sign out.
import { useState } from "react";
import { AlertTriangle, Info, Loader2, LogIn, RefreshCw } from "lucide-react";
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
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { accountKey, accountsApi, accountTitle, fableNeedsYou, freeResetRows, methodLabel, removeExplanation, sentence, type AccountStatus } from "@/lib/accounts";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { HarnessMark, InstallHint, LimitRow } from "./usage-bits";

const AMBER = "text-[color-mix(in_oklch,var(--state-waiting)_72%,var(--foreground))]";

function refresh(a: AccountStatus) {
  accountsApi.refresh(a.harness, a.name).catch((e) => toast.error(`Could not check ${accountTitle(a)}`, { description: (e as Error).message }));
}

export function AccountCard({
  a,
  now,
  starting,
  onConnect,
  multi = false,
}: {
  a: AccountStatus;
  now: number;
  /** Connect was clicked and the daemon has not answered yet */
  starting: boolean;
  /** start the sign-in, or show the one in progress */
  onConnect: (a: AccountStatus) => void;
  /** the harness has more than one account: show which new agents use, and Remove */
  multi?: boolean;
}) {
  const [askOut, setAskOut] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [askRemove, setAskRemove] = useState(false);
  const [weekly, setWeekly] = useState(false);
  const [resetting, setResetting] = useState(false);
  const [measuring, setMeasuring] = useState(false);
  const id = a.account ?? {};
  const method = methodLabel(id.method);
  const limits = a.limits ?? [];
  const login = a.login.state;
  const title = accountTitle(a);
  const canMeasure = a.harness === "antigravity" && a.connected && a.quota_measure_on_demand;

  const makeDefaultForNew = () =>
    accountsApi
      .setLaunchDefault(a.harness, a.name)
      .then(() => toast(`New ${a.label} agents use ${title}`))
      .catch((e) => toast.error(`Could not change ${a.label}'s account for new agents`, { description: (e as Error).message }));
  const remove = () =>
    accountsApi
      .remove(a.harness, a.name)
      .then(() => toast(`Removed ${title}`))
      .catch((e) => toast.error(`Could not remove ${title}`, { description: (e as Error).message }));

  const signOut = async () => {
    setSigningOut(true);
    try {
      await accountsApi.logout(a.harness, a.name);
      toast(`Signed out of ${title}`);
    } catch (e) {
      toast.error(`Could not sign out of ${title}`, { description: (e as Error).message });
    } finally {
      setSigningOut(false);
    }
  };

  const measure = async () => {
    setMeasuring(true);
    try {
      await accountsApi.measureAntigravityQuota(a.name);
    } catch (e) {
      toast.error(`Could not measure quota for ${title}`, { description: (e as Error).message });
    } finally {
      setMeasuring(false);
    }
  };

  return (
    <article
      data-account={accountKey(a)}
      aria-label={title}
      className={cn("flex min-w-0 flex-col rounded-xl border bg-card transition-colors duration-150", !a.installed && "bg-card/60")}
    >
      <header className="flex items-center gap-3 p-4 pb-3">
        <HarnessMark harness={a.harness} label={a.label} muted={!a.connected} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h2 className="min-w-0 truncate text-[0.875rem] font-semibold tracking-tight">{title}</h2>
            {a.connected && id.plan && (
              <span className="shrink-0 rounded-full bg-brand-soft px-2 text-2xs leading-[1.125rem] font-medium text-brand">{id.plan}</span>
            )}
            {multi && a.launch_default && (
              <span className="shrink-0 rounded-full border px-2 text-2xs leading-[1.125rem] text-muted-foreground" title="Agents launched without an account use this one">
                New agents
              </span>
            )}
          </div>
          {a.connected ? (
            <p className="mt-0.5 truncate text-xs text-muted-foreground" title={[id.email, id.org].filter(Boolean).join(" · ")}>
              {[id.email || "Signed in", method, id.org].filter(Boolean).join(" · ")}
            </p>
          ) : (
            <p className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
              {login === "running" ? (
                <>
                  <span className="size-1.5 animate-pulse-dot rounded-full bg-state-working" aria-hidden />
                  Signing in…
                </>
              ) : a.installed ? (
                <>
                  <span className="size-1.5 rounded-full bg-state-idle" aria-hidden />
                  Not connected
                </>
              ) : (
                <>
                  <span className="size-1.5 rounded-full border border-state-idle" aria-hidden />
                  Not installed
                </>
              )}
            </p>
          )}
        </div>
      </header>

      {a.error && (
        <p className={cn("mx-4 mb-3 flex items-start gap-1.5 rounded-md bg-state-waiting/10 px-2.5 py-1.5 text-xs", AMBER)} role="status">
          <AlertTriangle className="mt-px size-3.5 shrink-0" aria-hidden />
          <span className="min-w-0 [overflow-wrap:anywhere]">{sentence(a.error)}</span>
        </p>
      )}

      <div className="flex flex-1 flex-col px-4 pb-4">
        {fableNeedsYou(a) && (
          <p role="status" className={cn("mb-3 rounded-md bg-state-waiting/10 p-2.5 text-xs", AMBER)}>
            <strong>Needs you</strong> · {a.reset?.state === "needs_you" ? a.reset.message : `${title}'s Fable usage has reached 95%. Free Fable reset will be attempted at 99%.`}
          </p>
        )}
        {a.harness === "claude" && a.connected && a.account?.method !== "api_key" && (
          <div className="mb-4 space-y-2 rounded-md border p-2.5 text-xs" aria-label="Free resets">
            {freeResetRows(a, now).map((r) => (
              <p key={r.label}><strong>{r.label}</strong><br /><span className="text-muted-foreground">{r.detail}</span></p>
            ))}
            {a.reset && a.reset.state !== "needs_you" && <p role="status">{a.reset.message}</p>}
            <p className="text-muted-foreground">Fable resets automatically at 99% when a free reset is usable. Weekly resets require your switch each time.</p>
            <label className="flex items-center gap-2">
              <input type="checkbox" role="switch" checked={weekly} disabled={resetting} onChange={(e) => setWeekly(e.target.checked)} />
              Use one free Weekly reset for {id.email || title}
            </label>
            {weekly && <Button size="sm" disabled={resetting || !id.email} onClick={async () => {
              setResetting(true);
              try { await accountsApi.reset(a.name, id.email!, "weekly"); setWeekly(false); }
              catch (e) { toast.error("Weekly reset could not be requested", { description: (e as Error).message }); }
              finally { setResetting(false); }
            }}>Confirm Weekly reset at 99%</Button>}
            {a.reset?.state === "needs_you" && <p>Check /usage and free reset offers in Claude for this account. An unverified attempt is never retried automatically.</p>}
          </div>
        )}
        {a.connected ? (
          limits.length > 0 ? (
            <div className="space-y-4 pt-1">
              {limits.map((l) => (
                <LimitRow key={l.id} limit={l} now={now} />
              ))}
            </div>
          ) : canMeasure ? (
            <div className="space-y-1 pt-1 text-xs text-muted-foreground">
              <p>Quota not measured</p>
              {a.limits_note && <p>{sentence(a.limits_note)}</p>}
            </div>
          ) : a.refreshing && !a.checked_at ? (
            <div className="space-y-3 pt-1" aria-busy="true">
              <Skeleton className="h-3 w-2/3" />
              <Skeleton className="h-2 w-full rounded-full" />
            </div>
          ) : (
            <p className="flex items-start gap-1.5 pt-1 text-xs leading-5 text-muted-foreground">
              <Info className="mt-[3px] size-3.5 shrink-0 text-subtle-foreground" aria-hidden />
              <span>{sentence(a.limits_note) || `${title} reported no quota.`}</span>
            </p>
          )
        ) : a.installed ? (
          <div className="flex flex-1 flex-col gap-3 pt-1">
            <p className="text-[0.8125rem] leading-5 text-muted-foreground">Sign in to see how much of {title}'s quota you have used.</p>
            {login === "failed" && (
              <p className="flex items-start gap-1.5 text-xs text-state-error" role="status">
                <AlertTriangle className="mt-px size-3.5 shrink-0" aria-hidden />
                <span className="min-w-0 [overflow-wrap:anywhere]">{sentence(a.login.message) || "The last sign-in did not finish."}</span>
              </p>
            )}
            <div className="mt-auto space-y-2 pt-3">
              <Button variant="brand" size="lg" className="w-full" disabled={starting} onClick={() => onConnect(a)}>
                {starting || login === "running" ? <Loader2 className="size-4 animate-spin" /> : <LogIn className="size-4" />}
                {login === "running" ? "Continue signing in" : login === "failed" ? "Try again" : `Connect ${title}`}
              </Button>
              <p className="text-center text-2xs text-subtle-foreground">Uses {a.label}'s own sign-in, in your browser.</p>
            </div>
          </div>
        ) : (
          <div className="flex flex-1 flex-col gap-3 pt-1">
            <p className="text-[0.8125rem] leading-5 text-muted-foreground">
              <span className="font-medium text-foreground">Install, then connect.</span> {a.label} is not on this machine yet.
            </p>
            {a.install && <InstallHint text={a.install} label={a.label} />}
            <Button variant="outline" size="sm" className="self-start" disabled={a.refreshing} onClick={() => refresh(a)}>
              <RefreshCw className={cn(a.refreshing && "animate-spin")} />
              {a.refreshing ? "Checking…" : "Check again"}
            </Button>
          </div>
        )}
      </div>

      {a.connected && (
        <footer className={cn("flex items-center gap-1 border-t px-4 py-1.5", canMeasure && "flex-wrap")}>
          <span className="tabular min-w-0 flex-1 truncate text-2xs text-subtle-foreground">
            {a.refreshing ? "Checking…" : a.checked_at ? `Checked ${relativeTime(a.checked_at, now)}` : "Not checked yet"}
          </span>
          {canMeasure && (
            <Button variant="outline" size="xs" disabled={measuring} onClick={() => void measure()}>
              {measuring && <Loader2 className="animate-spin" />}
              {measuring ? "Measuring…" : "Measure"}
            </Button>
          )}
          <Tooltip>
            <TooltipTrigger asChild>
              <Button variant="ghost" size="icon-xs" className="text-muted-foreground" aria-label={`Check ${title} again`} disabled={a.refreshing} onClick={() => refresh(a)}>
                <RefreshCw className={cn(a.refreshing && "animate-spin")} />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Check again</TooltipContent>
          </Tooltip>
          <Button variant="ghost" size="xs" className="text-muted-foreground" disabled={signingOut || !!a.sign_out_reason} title={a.sign_out_reason} onClick={() => setAskOut(true)}>
            {signingOut && <Loader2 className="animate-spin" />}
            Sign out
          </Button>
        </footer>
      )}

      {/* Every card can be removed (the owner, 17 Nov): a named account leaves the list, a default one is hidden. */}
      <div className="flex items-center gap-1 border-t px-4 py-1.5">
        {multi && !a.launch_default && (
          <Button variant="ghost" size="xs" className="text-muted-foreground" onClick={() => void makeDefaultForNew()}>
            Use for new agents
          </Button>
        )}
        <Button variant="ghost" size="xs" className="ml-auto text-muted-foreground" disabled={login === "running"} onClick={() => setAskRemove(true)}>
          Remove
        </Button>
      </div>

      <AlertDialog open={askRemove} onOpenChange={setAskRemove}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove {title}?</AlertDialogTitle>
            <AlertDialogDescription>{removeExplanation(a)}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => void remove()}>
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={askOut} onOpenChange={setAskOut}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Sign out of {title}?</AlertDialogTitle>
            <AlertDialogDescription>
              {title} on this machine signs out{id.email ? ` of ${id.email}` : ""}. Agents that use it stop working until you connect again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Stay signed in</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => void signOut()}>
              Sign out
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </article>
  );
}

export function AccountCardSkeleton() {
  return (
    <div className="flex flex-col rounded-xl border bg-card p-4" aria-hidden>
      <div className="flex items-center gap-3">
        <Skeleton className="size-9 rounded-[9px]" />
        <div className="flex-1 space-y-1.5">
          <Skeleton className="h-3.5 w-24" />
          <Skeleton className="h-3 w-40" />
        </div>
      </div>
      <div className="mt-5 space-y-5">
        {[0, 1].map((i) => (
          <div key={i} className="space-y-2">
            <Skeleton className="h-3 w-1/2" />
            <Skeleton className="h-2 w-full rounded-full" />
          </div>
        ))}
      </div>
    </div>
  );
}
