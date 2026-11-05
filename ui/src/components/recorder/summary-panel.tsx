// The Summary panel on a recording's page: the summary of the recording (written from its
// transcripts and still frames, after it stopped), its key points, decisions, action items and
// chapters (a click seeks the player), where it is while it is being made, and Regenerate.
import { useEffect, useState } from "react";
import { AlertTriangle, CheckSquare, ListTree, Loader2, RefreshCw, Sparkles } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useNow } from "@/hooks/use-now";
import { formatSize } from "@/lib/attachments";
import { modelName, relativeTime } from "@/lib/format";
import type { Recording } from "@/lib/recording-types";
import { clock, isLive } from "@/lib/recording-types";
import { navigate, paths } from "@/lib/router";
import { summaryApi, summaryPending, useSummaries, type RecordingSummary } from "@/lib/summaries";
import { ApiError } from "@/lib/transport";
import { cn } from "@/lib/utils";
import { useVoice, type VoiceStatus } from "@/lib/voice";

/** Voice's download right now, as "12 MB of 57 MB" ("" when nothing downloads). */
function voiceDownload(s: VoiceStatus | null): string {
  if (!s?.busy) return "";
  const parts = [s.engine.downloading, ...s.models.map((m) => m.downloading), s.vad.downloading].filter(Boolean);
  const done = parts.reduce((a, p) => a + (p?.done ?? 0), 0);
  const total = parts.reduce((a, p) => a + (p?.total ?? 0), 0);
  return total > 0 ? `${formatSize(done)} of ${formatSize(total)}` : "";
}

function plural(n: number, one: string, many: string) {
  return `${n} ${n === 1 ? one : many}`;
}

function Progress({ x }: { x: RecordingSummary }) {
  const voice = useVoice((s) => s.status);
  const dl = voiceDownload(voice);
  const text =
    x.state === "transcribing"
      ? x.note && dl
        ? `${x.note.replace(/…$/, "")}: ${dl}`
        : x.note || "Transcribing on this machine, then the summary is written…"
      : x.state === "queued"
        ? "Waiting for its turn: one summary at a time…"
        : "Writing the summary…";
  return (
    <p className="flex items-start gap-2 text-muted-foreground" data-summary-progress={x.state}>
      <Loader2 className="mt-0.5 size-3.5 shrink-0 animate-spin" />
      <span>{text}</span>
    </p>
  );
}

function List({ title, items }: { title: string; items: React.ReactNode[] }) {
  if (!items.length) return null;
  return (
    <div>
      <h4 className="mb-1 text-2xs font-medium tracking-wide text-subtle-foreground uppercase">{title}</h4>
      <ul className="space-y-1">
        {items.map((it, i) => (
          <li key={i} className="flex gap-2">
            <span className="mt-[0.55em] size-1 shrink-0 rounded-full bg-subtle-foreground" aria-hidden />
            <span className="min-w-0">{it}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function Body({ x, onSeek, faded }: { x: RecordingSummary; onSeek: (n: number, ms: number) => void; faded?: boolean }) {
  return (
    <div className={cn("space-y-4", faded && "opacity-60")}>
      <p className="leading-relaxed">{x.summary}</p>
      <div className="grid gap-4 sm:grid-cols-2">
        <List title="Key points" items={x.key_points} />
        <List title="Decisions" items={x.decisions} />
        <List
          title="Action items"
          items={x.action_items.map((a) => (
            <span key={a.what}>
              {a.who && <span className="font-medium">{a.who}: </span>}
              {a.what}
            </span>
          ))}
        />
      </div>
      {x.chapters.length > 0 && (
        <div>
          <h4 className="mb-1 flex items-center gap-1.5 text-2xs font-medium tracking-wide text-subtle-foreground uppercase">
            <ListTree className="size-3" />
            Chapters
          </h4>
          <ol className="space-y-0.5" aria-label="Chapters">
            {x.chapters.map((c, i) => (
              <li key={i}>
                <button
                  type="button"
                  onClick={() => onSeek(c.segment, c.offset_ms)}
                  className="group flex w-full items-baseline gap-2.5 rounded px-1 py-0.5 text-left outline-none hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring"
                  data-chapter={i}
                >
                  <span className="tabular w-12 shrink-0 font-mono text-2xs text-subtle-foreground group-hover:text-foreground">{clock(c.t_ms)}</span>
                  <span className="min-w-0">{c.title}</span>
                </button>
              </li>
            ))}
          </ol>
        </div>
      )}
    </div>
  );
}

export function SummaryPanel({ rec, onSeek }: { rec: Recording; onSeek: (n: number, ms: number) => void }) {
  const x = useSummaries((s) => s.byId[rec.id]);
  const auto = useSummaries((s) => s.settings?.auto);
  const [loading, setLoading] = useState(!x);
  const [missing, setMissing] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const live = isLive(rec);
  const now = useNow(60_000);

  useEffect(() => {
    let gone = false;
    setError("");
    summaryApi
      .get(rec.id)
      .then(() => !gone && (setLoading(false), setMissing(false)))
      .catch((e) => {
        if (gone) return;
        setLoading(false);
        if (e instanceof ApiError && e.status === 501) setMissing(true);
        else if (!(e instanceof ApiError && e.status === 404)) setError((e as Error).message);
      });
    if (useSummaries.getState().settings === null) summaryApi.settings().catch(() => {});
    return () => {
      gone = true;
    };
  }, [rec.id, rec.state]);

  async function regenerate() {
    setBusy(true);
    try {
      await summaryApi.regenerate(rec.id);
    } catch (e) {
      toast.error("Not summarized", { description: (e as Error).message, duration: 10000 });
    } finally {
      setBusy(false);
    }
  }

  const pending = summaryPending(x?.state);
  const has = !!x?.summary;
  const canRun = !live && !missing && !pending && !loading;
  return (
    <section aria-label="Summary" className="rounded-xl border bg-card" data-summary-state={x?.state ?? (loading ? "loading" : "none")}>
      <div className="flex h-10 items-center gap-2 border-b px-3.5">
        <Sparkles className="size-4 text-muted-foreground" />
        <h3 className="text-[0.8125rem] font-medium">Summary</h3>
        {canRun && (
          <Button variant="ghost" size="xs" className="ml-auto" onClick={() => void regenerate()} disabled={busy}>
            {busy ? <Loader2 className="animate-spin" /> : <RefreshCw />}
            {has ? "Regenerate" : "Summarize"}
          </Button>
        )}
      </div>
      <div className="space-y-3 px-3.5 py-3 text-[0.8125rem]">
        {loading ? (
          <Skeleton className="h-16" />
        ) : missing ? (
          <p className="text-muted-foreground">Summaries aren't available on this daemon yet.</p>
        ) : error ? (
          <p className="text-state-error">{error}</p>
        ) : live ? (
          <p className="text-muted-foreground">When you stop the recording, it is transcribed and a summary is written here, on this machine.</p>
        ) : !x || x.state === "none" ? (
          <p className="text-muted-foreground">
            No summary yet.{" "}
            {auto === false ? (
              <>
                Summaries of new recordings are off (
                <button type="button" className="underline hover:text-foreground" onClick={() => navigate(paths.settings() + "?section=recordings")}>
                  Settings → Recordings
                </button>
                ).
              </>
            ) : (
              "Summarize transcribes it on this machine first, then writes it."
            )}
          </p>
        ) : (
          <>
            {pending && <Progress x={x} />}
            {x.state === "failed" && (
              <p className="flex items-start gap-2 rounded-md border border-state-error/30 bg-state-error/10 px-2.5 py-2 text-xs">
                <AlertTriangle className="mt-px size-3.5 shrink-0 text-state-error" />
                <span className="[overflow-wrap:anywhere]">
                  {has ? "Couldn't write a new summary. " : "Couldn't write the summary. "}
                  {x.error}
                </span>
              </p>
            )}
            {x.state === "skipped" && <p className="text-muted-foreground">{x.error}</p>}
            {has && <Body x={x} onSeek={onSeek} faded={pending} />}
            {has && (
              <p className="flex flex-wrap items-center gap-x-1.5 text-2xs text-subtle-foreground">
                <CheckSquare className="size-3" />
                <span>
                  By {modelName(x.model) || x.model || "the local model"} from {plural(x.transcripts, "transcript", "transcripts")} and {plural(x.frames, "frame", "frames")}
                  {x.parts > 1 ? `, in ${x.parts} parts` : ""}
                </span>
                {x.finished_at ? <span>· {relativeTime(x.finished_at, now)}</span> : null}
              </p>
            )}
            {x.note && !pending && <p className="text-2xs text-muted-foreground">{x.note}</p>}
          </>
        )}
      </div>
    </section>
  );
}
