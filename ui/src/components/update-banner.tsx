// After an update the daemon runs the new release while the desktop window still shows the UI it
// started with: this bar offers to restart the window. Browsers just reload.
import { useEffect, useState } from "react";
import { ArrowUpCircle, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { useStore } from "@/lib/store";
import { relaunchWindow, windowVersion } from "@/lib/update";
import { useCapture } from "@/lib/recorder";

export function WindowUpdateBanner() {
  const transport = useStore((s) => s.transport);
  const daemonVersion = useStore((s) => s.version);
  const [win, setWin] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // a restart now would end the recording this window captures (the daemon's own install already waits for it)
  const recording = useCapture((s) => s.phase !== "idle" || s.backlog > 0);
  const desktop = !!transport?.desktop && transport.kind !== "mock";

  useEffect(() => {
    if (desktop) void windowVersion().then(setWin);
  }, [desktop]);

  if (!desktop || !win || !daemonVersion || win === daemonVersion) return null;
  return (
    <div role="status" className="flex shrink-0 items-center gap-3 border-b bg-brand-soft px-4 py-2 text-[0.8125rem] text-foreground md:px-6">
      <ArrowUpCircle className="size-4 shrink-0 text-brand" />
      <span className="min-w-0 flex-1">
        tmx was updated to <span className="font-mono">{daemonVersion}</span>.{" "}
        {recording ? "Restart the window after the recording." : "Restart the window to finish"} (this window still runs{" "}
        <span className="font-mono">{win}</span>).
      </span>
      <Button
        size="xs"
        disabled={busy || recording}
        onClick={() => {
          setBusy(true);
          relaunchWindow().catch((e: Error) => {
            setBusy(false);
            toast.error(e.message);
          });
        }}
      >
        {busy && <Loader2 className="animate-spin" />}
        Restart window
      </Button>
    </div>
  );
}

/** A dot next to the version in the sidebar when a newer release is on its way. */
export function UpdateDot({ state }: { state?: string }) {
  if (!state || !["available", "downloading", "ready", "installing"].includes(state)) return null;
  return <span className="inline-block size-1.5 rounded-full bg-brand" aria-label="An update is available" />;
}
