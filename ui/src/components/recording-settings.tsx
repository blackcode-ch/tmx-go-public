// Settings → Recordings: what happens when a recording stops. It is transcribed on this machine
// (voice is set up first when it is missing), then Claude writes a summary from the transcript
// and the still frames, with the chosen model.
import { useEffect, useState } from "react";
import { TriangleAlert } from "lucide-react";
import { toast } from "sonner";
import { Section } from "@/components/page";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { modelName } from "@/lib/format";
import { useStore } from "@/lib/store";
import { summaryApi, useSummaries } from "@/lib/summaries";
import { loadVoice, useVoice, voiceApi } from "@/lib/voice";

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

export function RecordingSettingsSection() {
  const st = useSummaries((s) => s.settings);
  const voice = useVoice((s) => s.status);
  const status = useStore((s) => s.status);
  const [missing, setMissing] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);

  useEffect(() => {
    if (status !== "ready") return;
    summaryApi.settings().then(
      () => setMissing(false),
      () => setMissing(true),
    );
    void loadVoice();
  }, [status]);

  if (missing || !st) return null; // a daemon without summaries

  async function run(what: string, fn: () => Promise<unknown>) {
    setBusy(what);
    try {
      await fn();
      await summaryApi.settings(); // auto_transcribe is voice's: read it back
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(null);
    }
  }

  const models = st.models.includes(st.model) ? st.models : [...st.models, st.model];
  const autoTranscribe = voice ? voice.settings.auto_transcribe : st.auto_transcribe;
  const canTranscribe = st.voice && (voice?.engine.available ?? true);
  return (
    <div id="recordings" className="scroll-mt-6">
      <Section title="Recordings">
        <div className="divide-y rounded-xl border bg-card px-4">
          {canTranscribe && (
            <Row
              label="Transcribe finished recordings"
              hint="On this machine, when a recording stops. If voice isn't set up yet, it is set up first (about 60 MB)."
            >
              <Switch
                checked={autoTranscribe}
                disabled={!!busy}
                onCheckedChange={(v) => void run("transcribe", () => voiceApi.configure({ auto_transcribe: v }))}
                aria-label="Transcribe finished recordings"
                className="mt-1"
              />
            </Row>
          )}
          <Row label="Summarize finished recordings" hint="A short summary from the transcript and still frames of the screen or camera, written by the local model on this machine (recordings are client content).">
            <div className="space-y-1.5 pt-1">
              <Switch
                checked={st.auto}
                disabled={!!busy}
                onCheckedChange={(v) => void run("auto", () => summaryApi.configure({ auto: v }))}
                aria-label="Summarize finished recordings"
              />
              {st.claude_error && (
                <p className="flex items-start gap-1.5 text-xs text-state-waiting">
                  <TriangleAlert className="mt-px size-3.5 shrink-0" />
                  <span className="[overflow-wrap:anywhere]">Claude Code wasn't found, so summaries can't be written: {st.claude_error}</span>
                </p>
              )}
            </div>
          </Row>
          <Row label="Summary model" hint="Haiku is the cheapest and fast. It runs through Claude Code on this machine, with your account.">
            <Select value={st.model} onValueChange={(v) => void run("model", () => summaryApi.configure({ model: v }))}>
              <SelectTrigger className="w-56" aria-label="Summary model">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {models.map((m) => (
                  <SelectItem key={m} value={m}>
                    {modelName(m)}
                    {m === st.default_model ? " (cheapest)" : ""}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Row>
        </div>
      </Section>
    </div>
  );
}
