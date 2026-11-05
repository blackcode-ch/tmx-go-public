// Connect an AI account: the harness's own sign-in runs in a terminal on the daemon; this shows
// that terminal (it opens the browser itself, and may ask for a pasted code) and follows
// login.state on accounts.updated. Succeeded closes the dialog with a toast; closing while it
// runs asks first, then cancels it (DELETE).
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { AlertTriangle, Check, Loader2, RotateCw } from "lucide-react";
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
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { accountsApi, accountTitle, loadAccounts, sentence, useAccount, type AccountStatus } from "@/lib/accounts";
import { loginTerminalPath } from "@/lib/transport";
import { cn } from "@/lib/utils";
import { HarnessMark } from "./usage-bits";

const TerminalView = lazy(() => import("@/components/terminal-view").then((m) => ({ default: m.TerminalView })));

export interface Connecting {
  harness: string;
  /** the account, by its name among the harness's */
  account: string;
  /** the daemon's sign-in session; a new one remounts the terminal */
  session: string;
}

export function ConnectDialog({ connecting: current, onClose, onRetry }: { connecting: Connecting | null; onClose: () => void; onRetry: (a: AccountStatus) => void }) {
  // The last session stays on screen while the dialog fades out.
  const last = useRef<Connecting | null>(current);
  if (current) last.current = current;
  const connecting = last.current;
  const a = useAccount(connecting?.harness ?? "", connecting?.account);
  const [ask, setAsk] = useState(false);
  // What we know of this session: "starting" until the daemon reports it running.
  const saw = useRef(false);
  const done = useRef("");
  const state = a?.login.state;
  const phase = state === "running" ? "running" : saw.current ? state : "starting";

  useEffect(() => {
    saw.current = a?.login.state === "running";
    // The event that says "running" may have gone by before this opened: ask once.
    const t = setTimeout(() => !saw.current && void loadAccounts(), 1500);
    return () => clearTimeout(t);
  }, [connecting?.session]); // eslint-disable-line react-hooks/exhaustive-deps -- once per session

  useEffect(() => {
    if (!a || !current) return;
    if (a.login.state === "running") {
      saw.current = true;
      return;
    }
    if (!saw.current) return;
    if (a.login.state === "succeeded" && a.connected && done.current !== current.session) {
      done.current = current.session; // once: the toast does not repeat on the next render
      const id = a.account ?? {};
      toast.success(`${a.label} connected`, { description: [id.plan, id.email].filter(Boolean).join(" · ") || undefined });
      onClose();
    }
  }, [a, current, onClose]);

  const close = () => {
    if (phase === "running" || phase === "starting") setAsk(true);
    else onClose();
  };

  const cancel = () => {
    if (!connecting) return;
    const label = a?.label ?? connecting.harness;
    accountsApi.cancelLogin(connecting.harness, connecting.account).catch((e) => toast.error(`Could not stop ${label}'s sign-in`, { description: (e as Error).message }));
    setAsk(false);
    onClose();
  };

  const label = a ? accountTitle(a) : (connecting?.harness ?? "");
  return (
    <>
      <Dialog open={!!current} onOpenChange={(v) => !v && close()}>
        <DialogContent
          className="gap-0 overflow-hidden p-0 sm:max-w-[42.5rem]"
          onInteractOutside={(e) => e.preventDefault()}
          // Escape in the terminal belongs to the sign-in (its menus use it).
          onEscapeKeyDown={(e) => (e.target as Element | null)?.closest?.(".xterm") && e.preventDefault()}
          data-connect-dialog
        >
          <DialogHeader className="flex-row items-center gap-3 space-y-0 border-b px-5 py-4 pr-12 text-left">
            {connecting && <HarnessMark harness={connecting.harness} label={label} className="size-8 text-[0.75rem]" />}
            <div className="min-w-0">
              <DialogTitle className="text-[0.9375rem]">Connect {label}</DialogTitle>
              <DialogDescription className="mt-0.5 text-xs">Finish signing in in your browser. If it asks for a code, paste it here.</DialogDescription>
            </div>
          </DialogHeader>
          <div className="flex h-[18.75rem] flex-col bg-background p-3 sm:h-[21.25rem]">
            {connecting && (
              <Suspense fallback={<div className="flex-1" />}>
                <TerminalView key={connecting.session} path={loginTerminalPath(connecting.harness, connecting.account)} ended={null} className="p-0 md:p-0" />
              </Suspense>
            )}
          </div>
          <div className="flex min-h-[3.25rem] flex-wrap items-center gap-x-3 gap-y-2 border-t px-5 py-2.5" aria-live="polite">
            <div className="min-w-0 flex-1 basis-56">
              <Status phase={phase} message={sentence(a?.login.message)} label={label} />
            </div>
            <div className="ml-auto flex shrink-0 items-center gap-1.5">
              {phase === "failed" || phase === "idle" ? (
                <>
                  <Button variant="ghost" size="sm" onClick={onClose}>
                    Close
                  </Button>
                  <Button variant="brand" size="sm" onClick={() => a && onRetry(a)}>
                    <RotateCw />
                    Try again
                  </Button>
                </>
              ) : (
                <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={close}>
                  Cancel
                </Button>
              )}
            </div>
          </div>
        </DialogContent>
      </Dialog>

      <AlertDialog open={ask} onOpenChange={setAsk}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Stop signing in to {label}?</AlertDialogTitle>
            <AlertDialogDescription>The sign-in in progress stops. You can connect again any time.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep signing in</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={cancel}>
              Stop
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function Status({ phase, message, label }: { phase: string | undefined; message?: string; label: string }) {
  const row = "flex min-w-0 items-start gap-2 text-xs";
  if (phase === "failed")
    return (
      <p className={cn(row, "text-state-error")}>
        <AlertTriangle className="mt-px size-3.5 shrink-0" aria-hidden />
        <span className="min-w-0 [overflow-wrap:anywhere]">{message || "The sign-in did not finish."}</span>
      </p>
    );
  if (phase === "succeeded")
    return (
      <p className={cn(row, "text-state-ok")}>
        <Check className="size-3.5 shrink-0" aria-hidden />
        Signed in
      </p>
    );
  if (phase === "idle")
    return <p className={cn(row, "text-muted-foreground")}>The sign-in stopped.</p>;
  if (phase === "running")
    return (
      <p className={cn(row, "text-muted-foreground")}>
        <span className="mt-[0.3125rem] size-1.5 shrink-0 animate-pulse-dot rounded-full bg-state-working" aria-hidden />
        <span className="min-w-0">{message || "Waiting for you to finish signing in…"}</span>
      </p>
    );
  return (
    <p className={cn(row, "text-muted-foreground")}>
      <Loader2 className="size-3.5 shrink-0 animate-spin" aria-hidden />
      Starting {label}'s sign-in…
    </p>
  );
}
