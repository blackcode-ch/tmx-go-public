// Settings → Peer-to-peer: turn it on/off per machine (owner-only, PIN-gated), and show the
// running node's status (GET /api/p2p) and its paired peers (GET /api/p2p/net/peers).
import { useEffect, useState } from "react";
import { Loader2, RotateCw, TriangleAlert, Users, Wifi, WifiOff } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Section } from "@/components/page";
import { api } from "@/lib/api";
import { reachabilityLabel, savedVsLiveBanner, shouldPollP2P } from "@/lib/p2p-format";
import type { P2PPeerRow, P2PStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

function Card({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("rounded-xl border bg-card", className)}>{children}</div>;
}

export function P2PSection() {
  const [status, setStatus] = useState<P2PStatus | null>(null);
  const [peers, setPeers] = useState<P2PPeerRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [restarting, setRestarting] = useState(false);

  useEffect(() => {
    let active = true;
    const load = () => {
      void api.getP2P().then((s) => { if (active) setStatus(s); }, () => {});
      void api.getP2PPeers().then((p) => { if (active) setPeers(p); }, () => { if (active) setPeers([]); });
    };
    load();
    const onFocus = () => load();
    const onVisible = () => { if (document.visibilityState === "visible") load(); };
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      active = false;
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, []);

  useEffect(() => {
    if (!shouldPollP2P(status)) return;
    let active = true;
    const timer = setInterval(() => {
      void api.getP2P().then((s) => { if (active) setStatus(s); }, () => {});
    }, 5000);
    return () => { active = false; clearInterval(timer); };
    // Only re-arm when restart_to_apply itself flips, not on every status refresh.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status?.restart_to_apply]);

  if (!status) return null;
  const banner = savedVsLiveBanner(status);

  async function toggle(enabled: boolean) {
    setBusy(true);
    try {
      const st = await api.putP2PConfig(enabled);
      if (st) setStatus(st);
      toast.success(enabled ? "Peer-to-peer saved: on" : "Peer-to-peer saved: off");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function restartNow() {
    setRestarting(true);
    try {
      await api.restartP2P();
      toast.success("Restarting - back in about 15 seconds");
    } catch (e) {
      toast.error((e as Error).message);
      setRestarting(false);
      return;
    }
    // The daemon drops the connection mid-restart; poll until it answers with the saved
    // setting live, or give up and say so - "Restarting" forever with no sign of life otherwise.
    const deadline = Date.now() + 60_000;
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 2000));
      try {
        const s = await api.getP2P();
        setStatus(s);
        if (!s.restart_to_apply) {
          setRestarting(false);
          return;
        }
      } catch {
        // still down, or still restarting - keep polling
      }
    }
    setRestarting(false);
    toast.error("Restart did not come back within a minute - check the daemon.");
  }

  return (
    <Section title="Peer-to-peer">
      <Card className="divide-y">
        {banner && (
          <div className="flex flex-col gap-2 bg-amber-500/10 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
            <p className="flex items-center gap-1.5 text-xs font-medium text-amber-700 dark:text-amber-400">
              <TriangleAlert className="size-3.5 shrink-0" />
              {banner}
            </p>
            <Button size="sm" variant="outline" disabled={restarting} onClick={() => void restartNow()} className="shrink-0">
              {restarting ? <Loader2 className="animate-spin" /> : <RotateCw />}
              Restart now
            </Button>
          </div>
        )}
        <div className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-start">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-1.5 text-sm font-medium">
              {status.enabled ? <Wifi className="size-4 text-brand" /> : <WifiOff className="size-4 text-subtle-foreground" />}
              Peer-to-peer
              <span
                className={cn(
                  "ml-1 rounded-md border px-1.5 py-px text-2xs font-medium",
                  status.enabled ? "border-brand/30 bg-brand/10 text-foreground" : "text-muted-foreground",
                )}
              >
                {reachabilityLabel(status)}
              </span>
            </div>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {status.production
                ? "Connect directly with other tmx installs: pairing, chats and files, with your own mesh instead of a central server."
                : "Needs the production profile (config.json \"profile\": \"production\"); it stays off in trial however this is set."}
            </p>
          </div>
          <Switch
            checked={status.saved_enabled}
            disabled={busy || (!status.saved_enabled && !status.production)}
            onCheckedChange={(v) => void toggle(v)}
            aria-label="Peer-to-peer"
          />
        </div>
        {status.enabled && (
          <div className="px-4 py-3 text-xs text-muted-foreground">
            <div className="grid gap-1 sm:grid-cols-2">
              <div>Name: <span className="text-foreground">{status.name || "(none set)"}</span></div>
              <div>Peer ID: <span className="font-mono text-foreground">{status.peer_id.slice(0, 16)}…</span></div>
            </div>
            {status.addrs.length > 0 && (
              <div className="mt-1.5">
                <div className="mb-1 font-medium text-muted-foreground">Listening on</div>
                <ul className="space-y-0.5">
                  {status.addrs.map((a) => (
                    <li key={a} className="font-mono">{a}</li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}
        {status.enabled && (
          <div className="px-4 py-3">
            <div className="mb-1.5 flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
              <Users className="size-3.5" />
              Paired peers ({peers.length})
            </div>
            {peers.length === 0 ? (
              <p className="text-xs text-muted-foreground">No paired peers yet.</p>
            ) : (
              <ul className="space-y-1 text-xs">
                {peers.map((p) => (
                  <li key={p.peer_id} className="flex items-center justify-between font-mono">
                    <span className="truncate">{p.peer_id.slice(0, 20)}…</span>
                    <span className="ml-2 shrink-0 text-muted-foreground">{p.reachability}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </Card>
    </Section>
  );
}
