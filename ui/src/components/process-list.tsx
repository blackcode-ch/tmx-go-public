import { useEffect, useState } from "react";
import { api, type ProcessInfo } from "@/lib/api";
import { asList } from "@/lib/as-list";

export function ProcessList({ agent, projectTools = false }: { agent?: string; projectTools?: boolean }) {
  const [rows, setRows] = useState<ProcessInfo[]>([]);
  const [error, setError] = useState("");
  const [warnings, setWarnings] = useState<string[]>([]);
  useEffect(() => {
    if (agent || projectTools) return;
    let active = true;
    void api.systemWarnings().then((v) => { if (active) setWarnings(asList<string>(v)); }, () => { if (active) setWarnings(["System warnings are unavailable."]); });
    return () => { active = false; };
  }, [agent, projectTools]);
  useEffect(() => {
    let active = true;
    const load = () => (agent ? api.processes(agent) : projectTools ? api.processTools() : api.systemTools()).then(
      (r) => { if (active) { const p = asList<ProcessInfo>(r); setRows(projectTools ? p.filter((v) => v.promoted) : p); setError(""); } },
      () => { if (active) setError("Process status is unavailable."); },
    );
    void load();
    const timer = setInterval(() => { void load(); }, 5000);
    return () => { active = false; clearInterval(timer); };
  }, [agent, projectTools]);
  const title = agent ? "Processes" : projectTools ? "Project tools" : "System tools";
  return <section className="my-6" aria-label={title}>
    <h2 className="mb-2 text-sm font-medium">{title}</h2>
    {warnings.map((warning) => <p key={warning} role="alert" className="mb-3 rounded border border-amber-500 p-3 text-sm">{warning}</p>)}
    {error ? <p className="text-sm text-muted-foreground">{error}</p> : rows.length === 0 ?
      <p className="text-sm text-muted-foreground">No processes recorded.</p> :
      <ul className="divide-y border-y">{rows.map((p) => <li key={p.id} className="py-2 text-sm">
        <div>{p.purpose} <span className="text-muted-foreground">· {p.state}</span></div>
        <div className="text-xs text-muted-foreground">{p.command}{p.pid > 0 ? ` · PID ${p.pid}` : ""}
          {p.started_at > 0 ? ` · Started ${new Date(p.started_at).toLocaleString()}` : ""}
          {p.restarts > 0 ? ` · Restarts ${p.restarts}` : ""}
          {p.category !== "system_tool" ? ` · Owner ${p.owner_id}` : ""}
          {p.promoted && p.project_id ? ` · Project ${p.project_id}` : ""}
        </div>
      </li>)}</ul>}
  </section>;
}
