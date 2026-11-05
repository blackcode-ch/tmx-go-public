// Overview: other local daemons merged behind the per-host views (WP4; see lib/fleet-hosts.ts
// for the loopback-only, no-p2p scope). Every value read from a host is that install's own
// data - rendered here as plain text only, never as HTML.
import { useEffect } from "react";
import { Waypoints } from "lucide-react";
import { StateDot } from "@/components/state";
import { loadFleetHosts, useFleetHosts } from "@/lib/fleet-hosts";
import { hostHeadLine, hostStatusLine } from "@/lib/fleet-hosts-format";
import { bytes, pct } from "@/lib/resources";

const POLL_MS = 15_000;

/** Overview: a compact card per other local daemon, behind the per-host views above it. */
export function FleetHostsStrip() {
  const hosts = useFleetHosts((s) => s.hosts);
  useEffect(() => {
    void loadFleetHosts();
    const id = setInterval(() => void loadFleetHosts(), POLL_MS);
    return () => clearInterval(id);
  }, []);
  if (hosts.length === 0) return null;
  return (
    <section aria-label="Other hosts">
      <h2 className="mb-2 flex items-center gap-2 text-[0.8125rem] font-medium text-muted-foreground">
        <Waypoints className="size-3.5" />
        Fleet
      </h2>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {hosts.map((h) => {
          const headLine = hostHeadLine(h);
          const isPeer = h.kind === "peer";
          const card = (
            <>
              <div className="flex items-center gap-2">
                <StateDot state={h.reachable ? "working" : "error"} />
                <span className="min-w-0 truncate text-[0.875rem] font-medium">{h.name}</span>
                <span className="tabular ml-auto shrink-0 text-2xs text-subtle-foreground">{isPeer ? "peer" : `:${h.port}`}</span>
              </div>
              {h.reachable ? (
                <>
                  <p className="truncate text-xs text-muted-foreground">{hostStatusLine(h)}</p>
                  {h.cpu !== undefined && (
                    <p className="tabular truncate text-2xs text-subtle-foreground">
                      {pct(h.cpu)} CPU · {bytes((h.mem_free_mb ?? 0) * 1024 * 1024)} free mem
                    </p>
                  )}
                  {headLine && <p className="truncate text-2xs text-subtle-foreground">{headLine}</p>}
                </>
              ) : (
                <p className="truncate text-xs text-muted-foreground">{h.error || "unreachable"}</p>
              )}
            </>
          );
          const className =
            "flex min-w-0 flex-col gap-1.5 rounded-xl border bg-card p-4 outline-none transition-colors duration-150 hover:border-border-strong focus-visible:ring-2 focus-visible:ring-ring";
          return isPeer ? (
            <div key={h.name} className={className}>
              {card}
            </div>
          ) : (
            <a key={h.name} href={`http://127.0.0.1:${h.port}/`} target="_blank" rel="noreferrer" className={className}>
              {card}
            </a>
          );
        })}
      </div>
    </section>
  );
}
