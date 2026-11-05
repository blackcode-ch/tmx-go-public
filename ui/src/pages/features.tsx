// Features (#/features): every feature the owner ordered, with status works / partial / missing and
// the evidence for it. Reads src/data/features.json; no server API yet.
import { useMemo, useState } from "react";
import { ListChecks, Search } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { EmptyState, PageBody, PageHeader, PageTitle, Section } from "@/components/page";
import raw from "@/data/features.json";
import {
  areaLabel,
  cleanFeatures,
  filterFeatures,
  groupByArea,
  statusCounts,
  STATUS_LABEL,
  STATUS_ORDER,
  type FeatureStatus,
  type StatusFilter,
} from "@/lib/features-format";
import { cn } from "@/lib/utils";

const ALL = cleanFeatures(raw);

const TONE: Record<FeatureStatus, string> = {
  works: "border-state-ok/40 bg-state-ok/10 text-state-ok",
  partial: "border-state-waiting/40 bg-state-waiting/10 text-foreground",
  missing: "border-state-error/40 bg-state-error/10 text-state-error",
  untested: "border-border bg-muted/60 text-muted-foreground",
  blocked: "border-brand/30 bg-brand-soft text-brand",
};

export function FeaturesPage() {
  const [status, setStatus] = useState<StatusFilter>("all");
  const [query, setQuery] = useState("");
  const counts = useMemo(() => statusCounts(ALL), []);
  const groups = useMemo(() => groupByArea(filterFeatures(ALL, status, query)), [status, query]);

  return (
    <>
      <PageHeader>
        <PageTitle>Features</PageTitle>
        <span className="tabular text-sm text-muted-foreground">{ALL.length} ordered</span>
      </PageHeader>
      <PageBody>
        <div className="mb-5 flex flex-wrap gap-1.5">
          {(["all", ...STATUS_ORDER] as StatusFilter[]).map((s) => (
            <Button key={s} size="sm" variant={status === s ? "secondary" : "ghost"} aria-pressed={status === s} onClick={() => setStatus(s)}>
              {s === "all" ? "All" : STATUS_LABEL[s]}
              <span className="tabular text-muted-foreground">{s === "all" ? ALL.length : counts[s]}</span>
            </Button>
          ))}
        </div>
        <div className="relative mb-6">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input className="pl-8" placeholder="Search features, areas, evidence" aria-label="Search features" value={query} onChange={(e) => setQuery(e.target.value)} />
        </div>
        {groups.length === 0 ? (
          <EmptyState icon={ListChecks} title="No features match">Try another status or search.</EmptyState>
        ) : (
          <div className="space-y-7">
            {groups.map((g) => (
              <Section key={g.area} title={areaLabel(g.area)} right={<span className="tabular text-2xs text-muted-foreground">{g.features.length}</span>}>
                <ul className="divide-y rounded-xl border bg-card">
                  {g.features.map((f) => (
                    <li key={f.id} className="px-4 py-3">
                      <div className="flex items-start gap-2">
                        <p className="min-w-0 flex-1 text-sm font-medium break-words">{f.title}</p>
                        <span className={cn("inline-flex h-5 shrink-0 items-center rounded-[5px] border px-1.5 text-2xs font-medium", TONE[f.status])}>
                          {STATUS_LABEL[f.status]}
                        </span>
                      </div>
                      {f.evidence && <p className="mt-1 text-sm break-words text-muted-foreground">{f.evidence}</p>}
                      <p className="tabular mt-1 text-2xs break-all text-subtle-foreground">
                        {/^\d+$/.test(f.id) ? `#${f.id}` : f.id}
                        {f.updated && ` · ${f.updated}`}
                      </p>
                    </li>
                  ))}
                </ul>
              </Section>
            ))}
          </div>
        )}
      </PageBody>
    </>
  );
}
