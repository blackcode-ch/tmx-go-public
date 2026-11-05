// Features page (#/features): grouping, filtering and counting for the list of ordered features.
// Pure, so it is unit tested (test/features-format.test.mjs); the data is src/data/features.json.
// untested = not run yet (not broken); blocked = needs a connected peer first.
export type FeatureStatus = "works" | "partial" | "missing" | "untested" | "blocked";

export interface Feature {
  id: string;
  title: string;
  area: string;
  status: FeatureStatus;
  /** test run, PR, date: what shows the status is true */
  evidence: string;
  /** YYYY-MM-DD */
  updated: string;
}

export const STATUS_LABEL: Record<FeatureStatus, string> = {
  works: "Works",
  partial: "Partial",
  missing: "Missing",
  untested: "Untested",
  blocked: "Blocked",
};
export const STATUS_ORDER: FeatureStatus[] = ["works", "partial", "missing", "untested", "blocked"];

export type StatusFilter = FeatureStatus | "all";

export function statusCounts(features: Feature[]): Record<FeatureStatus, number> {
  const c: Record<FeatureStatus, number> = { works: 0, partial: 0, missing: 0, untested: 0, blocked: 0 };
  for (const f of features) if (f.status in c) c[f.status]++;
  return c;
}

/** Status first, then a case-insensitive search over id, title, area and evidence. */
export function filterFeatures(features: Feature[], status: StatusFilter, query: string): Feature[] {
  const q = query.trim().toLowerCase();
  return features.filter((f) => {
    if (status !== "all" && f.status !== status) return false;
    if (!q) return true;
    return [f.id, f.title, f.area, f.evidence].some((s) => s.toLowerCase().includes(q));
  });
}

export interface FeatureGroup {
  area: string;
  features: Feature[];
}

/** Inside an area: what needs attention first (missing, blocked, partial, untested), done last. */
const SORT_RANK: Record<FeatureStatus, number> = { missing: 0, blocked: 1, partial: 2, untested: 3, works: 4 };

/** "messages-and-decisions" -> "Messages and decisions" */
export function areaLabel(area: string): string {
  const words = area.trim().replace(/[-_]+/g, " ");
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : "Other";
}

/** Groups by area, areas A-Z; inside an area by SORT_RANK, then by id. */
export function groupByArea(features: Feature[]): FeatureGroup[] {
  const rank = (s: FeatureStatus) => SORT_RANK[s];
  const byArea = new Map<string, Feature[]>();
  for (const f of features) {
    const area = f.area.trim() || "Other";
    const list = byArea.get(area);
    if (list) list.push(f);
    else byArea.set(area, [f]);
  }
  return [...byArea.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([area, list]) => ({
      area,
      features: [...list].sort((x, y) => rank(x.status) - rank(y.status) || x.id.localeCompare(y.id, undefined, { numeric: true })),
    }));
}

/** Keeps well-formed rows only, so one bad entry in the data file cannot blank the page. */
export function cleanFeatures(raw: unknown): Feature[] {
  if (!Array.isArray(raw)) return [];
  const out: Feature[] = [];
  for (const r of raw) {
    if (!r || typeof r !== "object") continue;
    const o = r as Record<string, unknown>;
    const id = typeof o.id === "number" ? String(o.id) : o.id;
    if (typeof id !== "string" || typeof o.title !== "string") continue;
    if (typeof o.status !== "string" || !STATUS_ORDER.includes(o.status as FeatureStatus)) continue;
    out.push({
      id,
      title: o.title,
      area: typeof o.area === "string" ? o.area : "",
      status: o.status as FeatureStatus,
      evidence: typeof o.evidence === "string" ? o.evidence : "",
      updated: typeof o.updated === "string" ? o.updated : "",
    });
  }
  return out;
}
