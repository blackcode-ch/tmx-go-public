import { useEffect, useState } from "react";
import { Pin, PinOff } from "lucide-react";
import { toast } from "sonner";
import { onBusEvent, useStore } from "@/lib/store";
import { changeMessagePin, loadMessagePins, pinMessage, useMessagePins, type MessageSelection } from "@/lib/message-pins";
import { relativeTime } from "@/lib/format";
import { useNow } from "@/hooks/use-now";
import type { TimelineEntry } from "@/lib/types";

const control = "min-h-11 min-w-11 rounded-md border px-3 py-2 text-base font-semibold focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50";

export function PinMessageButton({ selection }: { selection: MessageSelection }) {
  const pin = useMessagePins((s) => s.pins.find((p) => p.source === selection.source && p.state === "active"));
  const [busy, setBusy] = useState(false);
  return <button type="button" className={control + " inline-flex items-center gap-2"} disabled={busy} aria-pressed={!!pin}
    aria-label={pin ? "Unpin message" : "Pin message"} onClick={async () => {
      setBusy(true);
      try { if (pin) await changeMessagePin(pin.id, "unpinned"); else await pinMessage(selection); }
      catch (e) { toast.error((e as Error).message); }
      finally { setBusy(false); }
    }}>{pin ? <PinOff className="size-4" /> : <Pin className="size-4" />}{pin ? "Unpin" : "Pin"}</button>;
}

export function TimelinePin({ e }: { e: TimelineEntry }) {
  const name = useStore((s) => s.agents[e.agent_id]?.name ?? "Agent conversation");
  return <PinMessageButton selection={{ source: `timeline:${e.agent_id}:${e.seq}`, agent_id: e.agent_id,
    label: name, href: `#/agents/${encodeURIComponent(e.agent_id)}/conversation`, text: e.text, message_at: e.at }} />;
}

// A sibling of the page, outside all conversation scrollers: stays visible on phone and desktop.
export function MessagePinsStrip() {
  const { pins, error } = useMessagePins();
  const transport = useStore((s) => s.transport);
  const status = useStore((s) => s.status);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  useNow(30_000);
  useEffect(() => {
    void loadMessagePins();
    const off = onBusEvent((e) => { if (e.type === "message.pins.changed" || e.type === "snapshot") void loadMessagePins(); });
    const refresh = () => { if (!document.hidden) void loadMessagePins(); };
    window.addEventListener("focus", refresh);
    document.addEventListener("visibilitychange", refresh);
    return () => { off(); window.removeEventListener("focus", refresh); document.removeEventListener("visibilitychange", refresh); };
  }, [transport, status]);
  const active = pins.filter((p) => p.state === "active");
  const finished = pins.filter((p) => p.state === "done");
  const shown = done ? finished : active;
  async function change(id: string, state: "active" | "done" | "unpinned") {
    setBusy(id);
    try { await changeMessagePin(id, state); } catch (e) { toast.error((e as Error).message); } finally { setBusy(null); }
  }
  return <section aria-label="Pinned messages" className="shrink-0 border-b-2 border-brand bg-card px-3 py-2">
    <div className="flex flex-wrap items-center gap-2">
      <h2 className="mr-auto text-xl font-bold">Pinned · {active.length} unfinished</h2>
      <button className={control} aria-pressed={!done} onClick={() => setDone(false)}>Active</button>
      <button className={control} aria-pressed={done} onClick={() => setDone(true)}>Done ({finished.length})</button>
    </div>
    {error && <p role="status" className="text-base">Pins could not sync: {error} <button className={control} onClick={() => void loadMessagePins()}>Retry</button></p>}
    {!shown.length ? <p className="text-base text-muted-foreground">{done ? "Completed pins stay here." : "Tap Pin on a message to keep it here."}</p> :
      <ol className="mt-2 flex max-h-[30dvh] gap-3 overflow-auto pb-1" aria-label={done ? "Completed pins" : "Unfinished pins"}>
        {shown.map((p) => <li key={p.id} className="w-[min(85vw,32rem)] shrink-0 rounded-lg border-2 p-3">
          <a className="text-base font-semibold underline [overflow-wrap:anywhere]" href={p.href}>{p.label}</a>
          {p.message_at > 0 && <span className="ml-2 text-base" title={new Date(p.message_at).toLocaleString()}>Message {relativeTime(p.message_at)}</span>}
          <span className="ml-2 text-base" title={new Date(p.pinned_at).toLocaleString()}>Pinned {relativeTime(p.pinned_at)}</span>
          <p className="my-2 whitespace-pre-wrap text-xl leading-relaxed [overflow-wrap:anywhere]">{p.text}</p>
          <div className="flex flex-wrap gap-2">
            <button disabled={busy === p.id} className={control} onClick={() => void change(p.id, done ? "active" : "done")}>{done ? "Reopen" : "Mark done"}</button>
            <button disabled={busy === p.id} className={control} onClick={() => void change(p.id, "unpinned")}>Unpin</button>
          </div>
        </li>)}
      </ol>}
  </section>;
}
