import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { useStore } from "@/lib/store";

type JobCard = { id: string; card: boolean; state: string; reason: string; identity: { Key: string } };

// Keep last-good cards on transient failures. Only a successful server snapshot
// can remove a released card; requesting release does not optimistically hide it.
export function JobQuarantineCards() {
  const transport = useStore((s) => s.transport);
  const [cards, setCards] = useState<JobCard[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!transport) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    async function refresh() {
      try {
        const rows = await transport!.request<JobCard[]>("GET", "/api/jobs");
        if (!stopped) { setCards(rows.filter((r) => r.card)); setError(""); }
      } catch {
        if (!stopped) setError("Job status could not be refreshed. Existing cards are kept below.");
      } finally {
        if (!stopped) timer = setTimeout(refresh, 5000);
      }
    }
    void refresh();
    return () => { stopped = true; clearTimeout(timer); };
  }, [transport]);
  async function release(id: string) {
    try { await transport?.request("POST", `/api/jobs/${encodeURIComponent(id)}/release`); }
    catch { setError("Release could not be requested. Try again."); }
  }
  return <section className="mb-4 space-y-3" aria-label="Interrupted jobs">
    {error && <p role="status" className="text-sm text-muted-foreground">{error}</p>}
    {cards.map((job) => <article key={job.id} className="rounded-xl border bg-card p-4 space-y-2">
      <h2 className="font-medium">A requested audit needs attention</h2>
      <p className="text-sm break-words">Job reference (provided by requester): {job.identity.Key.slice(0, 160)}{job.identity.Key.length > 160 ? "…" : ""}</p>
      <p className="text-sm">{job.reason || "This job failed repeatedly and has been quarantined."}</p>
      <p className="text-sm text-muted-foreground">Inspect the cause before releasing it. Release allows a new request; it does not restart the audit.</p>
      <Button variant="outline" onClick={() => void release(job.id)}>Release quarantine</Button>
    </article>)}
  </section>;
}
