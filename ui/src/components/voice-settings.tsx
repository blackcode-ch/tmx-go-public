// Settings → Voice: local transcription with whisper.cpp. Set up downloads the engine and a model
// (verified, on this machine only); then the model and the language. Dictation in the message
// boxes and transcripts of recordings use it (Settings → Recordings: whether recordings are
// transcribed by themselves).
import { useEffect, useState } from "react";
import { AudioLines, CheckCircle2, Download, Loader2, Mic, Trash2, TriangleAlert } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Section } from "@/components/page";
import { formatSize } from "@/lib/attachments";
import { navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";
import { loadVoice, useVoice, voiceApi, VOICE_LANGUAGES, type VoiceModel, type VoiceProgress, type VoiceStatus } from "@/lib/voice";

function Row({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-2 py-3 sm:flex-row sm:items-start sm:gap-6">
      <div className="min-w-0 sm:w-56 sm:shrink-0 sm:pt-1.5">
        <div className="text-sm">{label}</div>
        {hint && <div className="mt-0.5 text-xs text-muted-foreground">{hint}</div>}
      </div>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

function Bar({ p }: { p: VoiceProgress }) {
  const pct = p.total > 0 ? Math.min(100, Math.round((p.done / p.total) * 100)) : 0;
  return (
    <div className="flex min-w-0 items-center gap-2" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}>
      <div className="h-1.5 w-full max-w-48 overflow-hidden rounded-full bg-muted">
        <div className="h-full rounded-full bg-brand transition-[width] duration-300" style={{ width: `${pct}%` }} />
      </div>
      <span className="shrink-0 text-2xs tabular-nums text-muted-foreground">
        {formatSize(p.done)} of {formatSize(p.total)}
      </span>
    </div>
  );
}

/** An error from the daemon as a sentence (they start in lower case, Go style). */
function sentence(e: string) {
  const t = e.trim();
  return t.charAt(0).toUpperCase() + t.slice(1) + (/[.!?)]$/.test(t) ? "" : ".");
}

function listing(items: string[]) {
  return items.length < 2 ? items.join("") : items.slice(0, -1).join(", ") + " and " + items[items.length - 1];
}

function headline(s: VoiceStatus): { icon: React.ReactNode; text: string; tone?: "warn" | "ok" } {
  if (s.settings.via_peer) return { icon: <CheckCircle2 className="size-4" />, text: `Audio and reports use ${s.settings.via_peer}. Recordings are kept locally while transcription is pending.`, tone: "ok" };
  if (!s.engine.available) return { icon: <TriangleAlert className="size-4" />, text: s.reason ?? "Not available on this machine.", tone: "warn" };
  if (s.busy) return { icon: <Loader2 className="size-4 animate-spin" />, text: "Downloading and checking…" };
  if (s.ready) return { icon: <CheckCircle2 className="size-4" />, text: "Voice is ready. Dictation and transcripts are made on this machine.", tone: "ok" };
  if (s.engine.error) return { icon: <TriangleAlert className="size-4" />, text: sentence(s.engine.error), tone: "warn" };
  const model = s.models.find((m) => m.name === s.settings.model);
  if (s.engine.installed && model && !model.installed) {
    if (model.error) return { icon: <TriangleAlert className="size-4" />, text: sentence(model.error), tone: "warn" };
    return { icon: <Mic className="size-4" />, text: `The ${model.label} model is not downloaded yet.` };
  }
  return { icon: <Mic className="size-4" />, text: "Voice is not set up yet. Then you can dictate into any message box, and recordings get transcripts." };
}

function ModelRow({ m, s, busy, run }: { m: VoiceModel; s: VoiceStatus; busy: string | null; run: (what: string, fn: () => Promise<unknown>) => void }) {
  const selected = m.name === s.settings.model;
  return (
    <li className="flex flex-col gap-1.5 py-2.5 sm:flex-row sm:items-start sm:gap-3">
      <label className="flex min-w-0 flex-1 cursor-pointer items-start gap-2.5">
        <input
          type="radio"
          name="voice-model"
          className="mt-1 size-3.5 accent-[var(--brand)]"
          checked={selected}
          disabled={!!busy}
          onChange={() => run("model", () => voiceApi.configure({ model: m.name }))}
        />
        <span className="min-w-0">
          <span className="text-sm">
            {m.label}
            {m.default && <span className="ml-1.5 text-2xs text-muted-foreground">recommended</span>}
          </span>
          <span className="block text-xs text-muted-foreground">
            {formatSize(m.size)} · about {m.ram_mb >= 1000 ? `${(m.ram_mb / 1000).toFixed(1)} GB` : `${m.ram_mb} MB`} of memory while it runs
          </span>
          {m.warning && <span className="mt-0.5 block text-xs text-state-waiting">{m.warning}</span>}
          {m.error && <span className="mt-0.5 block text-xs [overflow-wrap:anywhere] text-state-error">{sentence(m.error)}</span>}
        </span>
      </label>
      <div className="flex shrink-0 items-center gap-1.5 pl-6 sm:pl-0">
        {m.downloading ? (
          <Bar p={m.downloading} />
        ) : m.installed ? (
          <>
            <span className="text-xs text-muted-foreground">Downloaded</span>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={`Remove ${m.label}`}
              disabled={!!busy}
              onClick={() => run("rm-" + m.name, () => voiceApi.removeModel(m.name))}
            >
              <Trash2 />
            </Button>
          </>
        ) : (
          <Button size="sm" variant="outline" disabled={!!busy || !s.engine.available} onClick={() => run("get-" + m.name, () => voiceApi.getModel(m.name))}>
            <Download />
            Download
          </Button>
        )}
      </div>
    </li>
  );
}

export function VoiceSection() {
  const s = useVoice((x) => x.status);
  const status = useStore((x) => x.status);
  const [busy, setBusy] = useState<string | null>(null);
  const [peer, setPeer] = useState("");
  useEffect(() => { setPeer(s?.settings.via_peer ?? ""); }, [s?.settings.via_peer]);

  useEffect(() => {
    if (status === "ready") void loadVoice();
  }, [status]);

  if (!s) return null; // a daemon without voice

  async function run(what: string, fn: () => Promise<unknown>, done?: string) {
    setBusy(what);
    try {
      await fn();
      if (done) toast.success(done);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(null);
    }
  }

  const h = headline(s);
  const model = s.models.find((m) => m.name === s.settings.model);
  const needs = [
    !s.engine.installed && `the engine (whisper.cpp ${s.engine.version})`,
    model && !model.installed && `the ${model.label} model (${formatSize(model.size)})`,
    !s.vad.installed && "a small model that skips silence",
  ].filter(Boolean) as string[];
  return (
    <div id="voice" className="scroll-mt-6">
      <Section title="Voice">
		<SavedFleetTranscripts />
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <label htmlFor="voice-peer">Transcription machine</label>
          <input id="voice-peer" className="rounded border bg-background px-2 py-1" value={peer} onChange={(e) => setPeer(e.target.value)} placeholder="This machine (leave empty)" />
          <Button size="sm" disabled={!!busy} onClick={() => run("peer", () => voiceApi.configure({ via_peer: peer }))}>Save</Button>
          <p className="w-full text-xs text-muted-foreground">Enter a paired fleet machine name. Audio stays within your fleet; there is no cloud fallback.</p>
        </div>
        <div className="divide-y rounded-xl border bg-card px-4">
          <div className="flex flex-col gap-3 py-3.5 sm:flex-row sm:items-center">
            <div className={cn("flex min-w-0 flex-1 items-start gap-2.5 text-sm", h.tone === "warn" && "text-state-waiting", h.tone === "ok" && "text-foreground")}>
              <span className="mt-0.5 shrink-0">{h.icon}</span>
              <div className="min-w-0">
                <p className="[overflow-wrap:anywhere]">{h.text}</p>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  {s.settings.via_peer ? "All recordings use the authenticated fleet connection." : s.engine.available && !s.ready && needs.length > 0 && !s.busy
                    ? `Set up downloads and checks ${listing(needs)}. Nothing you say leaves this machine.`
                    : "Speech is transcribed by whisper.cpp on this machine; nothing is sent anywhere."}
                </p>
                {s.engine.needs_token && (
                  <p className="mt-1 text-xs text-muted-foreground">
                    The engine comes from tmx's private releases: add a GitHub token in{" "}
                    <button className="underline hover:text-foreground" onClick={() => navigate(paths.settings() + "?section=updates")}>
                      Settings → Updates
                    </button>
                    , then set up again.
                  </p>
                )}
              </div>
            </div>
            {s.engine.available && !s.ready && (
              <Button size="sm" className="shrink-0" disabled={!!busy || s.busy} onClick={() => void run("setup", () => voiceApi.setup())}>
                {busy === "setup" || s.busy ? <Loader2 className="animate-spin" /> : <Download />}
                {s.engine.error || (model && model.error) ? "Try again" : "Set up"}
              </Button>
            )}
          </div>

          {s.engine.available && (
            <Row label="Engine" hint="whisper.cpp, run once per transcription at low priority, one at a time.">
              <div className="pt-1.5 text-sm">
                {s.engine.downloading ? (
                  <Bar p={s.engine.downloading} />
                ) : s.engine.installed ? (
                  <span>
                    whisper.cpp <span className="font-mono text-[0.7812rem]">{s.engine.version}</span>
                    {s.engine.size ? <span className="text-muted-foreground"> · {formatSize(s.engine.size)}</span> : null}
                  </span>
                ) : (
                  <span className="text-muted-foreground">Not downloaded</span>
                )}
                {s.job && (
                  <p className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
                    <AudioLines className="size-3.5" />
                    {s.job.phase === "waiting"
                      ? `Waiting for a heavy slot${s.job.position ? ` (#${s.job.position})` : ""}: ${s.job.source}`
                      : `Transcribing ${s.job.source}${s.job.parts ? `, part ${s.job.part} of ${s.job.parts}` : ""}`}
                    {s.queued > 0 && ` · ${s.queued} waiting`}
                  </p>
                )}
              </div>
            </Row>
          )}

          {s.engine.available && (
            <Row label="Model" hint="Bigger models understand more and need more memory and time. All of them know many languages.">
              <ul className="-my-1 divide-y">
                {s.models.map((m) => (
                  <ModelRow key={m.name} m={m} s={s} busy={busy} run={(w, fn) => void run(w, fn)} />
                ))}
              </ul>
            </Row>
          )}

          {s.engine.available && (
            <>
              <Row label="Language" hint="What is spoken. Detect works for most; a fixed language is a little more reliable.">
                <Select value={s.settings.language} onValueChange={(v) => void run("language", () => voiceApi.configure({ language: v }))}>
                  <SelectTrigger className="w-56" aria-label="Spoken language">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {VOICE_LANGUAGES.map((l) => (
                      <SelectItem key={l.code} value={l.code}>
                        {l.label}
                      </SelectItem>
                    ))}
                    {!VOICE_LANGUAGES.some((l) => l.code === s.settings.language) && (
                      <SelectItem value={s.settings.language}>{s.settings.language}</SelectItem>
                    )}
                  </SelectContent>
                </Select>
              </Row>
              <Row
                label="Skip silence"
                hint={`Voice activity detection: fewer invented words in quiet parts.${s.vad.installed ? "" : ` Needs a small model (${formatSize(s.vad.size)}).`}`}
              >
                <div className="flex items-center gap-2 pt-1">
                  <Switch
                    checked={s.settings.vad}
                    disabled={!!busy}
                    onCheckedChange={(v) => void run("vad", () => voiceApi.configure({ vad: v }))}
                    aria-label="Skip silence"
                  />
                  {s.vad.downloading ? (
                    <Bar p={s.vad.downloading} />
                  ) : (
                    !s.vad.installed &&
                    s.settings.vad && (
                      <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => void run("vad-get", () => voiceApi.getModel(s.vad.name))}>
                        <Download />
                        Download
                      </Button>
                    )
                  )}
                </div>
              </Row>
            </>
          )}
        </div>
      </Section>
    </div>
  );
}

function SavedFleetTranscripts() {
  const connected = useStore((s) => s.status === "ready");
  const [items, setItems] = useState<Awaited<ReturnType<typeof voiceApi.transcripts>>>([]);
  useEffect(() => {
    if (!connected) return;
    let active = true;
    const refresh = () => void voiceApi.transcripts({ source_prefix: "upload:", limit: 20 }).then((rows) => { if (active) setItems(rows); }).catch(() => {});
    refresh();
    const timer = setInterval(refresh, 5000);
    return () => { active = false; clearInterval(timer); };
  }, [connected]);
  if (!items.length) return null;
  return <details className="mb-3 rounded border p-3">
    <summary>Saved fleet dictation and uploads ({items.length})</summary>
    <p className="my-2 text-xs text-muted-foreground">Audio stays on this machine until you delete its transcript. Pending work retries automatically.</p>
    {items.map((tr) => <article key={tr.id} className="border-t py-3">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs">{new Date(tr.created_at).toLocaleString()} · {tr.state === "done" ? "Ready" : tr.state === "failed" ? tr.error : "Transcription pending"}</span>
        <Button size="sm" variant="ghost" onClick={() => void voiceApi.deleteTranscript(tr.id).then(() => setItems((rows) => rows.filter((r) => r.id !== tr.id))).catch((e: Error) => toast.error(e.message))}>Delete</Button>
      </div>
      {tr.text && <p className="whitespace-pre-wrap text-sm">{tr.text}</p>}
      {tr.report && <details className="mt-2 text-sm"><summary>Report</summary><p className="whitespace-pre-wrap">{tr.report}</p></details>}
    </article>)}
  </details>;
}
