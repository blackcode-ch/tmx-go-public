// Settings → WhatsApp: linking the owner's own WhatsApp as a companion device (a QR code from the
// daemon, or a pairing code for a phone number), the state, the warning, the settings, Unlink.
// WaLinkPanel is the linking flow alone; the WhatsApp page shows it too while nothing is linked.
import { useEffect, useMemo, useState } from "react";
import { CheckCircle2, Loader2, Plus, QrCode, RefreshCw, Smartphone, TriangleAlert, Unlink, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { CopyButton } from "@/components/copy-button";
import { Section } from "@/components/page";
import { useNow } from "@/hooks/use-now";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";
import { isLinked, loadWhatsApp, saveWaSettings, sortedWaChats, useWa, waApi, waPhone } from "@/lib/whatsapp";
import type { WaSettings, WaStatus } from "@/lib/wa-types";
import { SyncBar, WaAvatar } from "./wa-bits";

/** The one-sentence warning shown before linking. */
export const WA_RISK =
  "WhatsApp doesn't officially support linking tools like this and can ban numbers that automate or send in bulk, so tmx sends slowly and agents ask you first.";

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

function sentence(e: string) {
  const t = e.trim();
  return t.charAt(0).toUpperCase() + t.slice(1) + (/[.!?)]$/.test(t) ? "" : ".");
}

async function attempt(fn: () => Promise<unknown>, done?: string) {
  try {
    await fn();
    if (done) toast.success(done);
    return true;
  } catch (e) {
    toast.error(sentence((e as Error).message || "that didn't work"));
    return false;
  }
}

// ---------------------------------------------------------------------------------------------
// linking

function Steps({ items }: { items: React.ReactNode[] }) {
  return (
    <ol className="space-y-1.5 text-sm">
      {items.map((it, i) => (
        <li key={i} className="flex gap-2.5">
          <span className="tabular flex size-5 shrink-0 items-center justify-center rounded-full bg-muted text-2xs font-semibold text-muted-foreground">{i + 1}</span>
          <span className="min-w-0 pt-px">{it}</span>
        </li>
      ))}
    </ol>
  );
}

function QRStep({ onPhone }: { onPhone: () => void }) {
  const qr = useWa((s) => s.qr);
  const now = useNow(1000);
  const left = qr?.expires_at ? Math.max(0, Math.ceil((qr.expires_at - now) / 1000)) : 0;
  const stale = !!qr?.png && !!qr.expires_at && left === 0;
  return (
    <div className="flex flex-col items-center gap-5 sm:flex-row sm:items-start">
      <div className="relative shrink-0">
        {qr?.png ? (
          <img
            src={qr.png}
            alt="QR code to link WhatsApp"
            className={cn("size-[13.5rem] rounded-xl bg-white p-2 shadow-sm ring-1 ring-black/10 [image-rendering:pixelated]", stale && "opacity-30")}
          />
        ) : (
          <div className="flex size-[13.5rem] flex-col items-center justify-center gap-2 rounded-xl border bg-muted/40 text-xs text-muted-foreground">
            <Loader2 className="size-5 animate-spin" />
            Getting a code from WhatsApp…
          </div>
        )}
        {stale && (
          <span className="absolute inset-0 flex items-center justify-center gap-1.5 text-xs font-medium">
            <Loader2 className="size-3.5 animate-spin" /> New code…
          </span>
        )}
      </div>
      <div className="min-w-0 flex-1 space-y-3">
        <p className="text-sm font-medium">Scan this with WhatsApp on your phone</p>
        <Steps
          items={[
            "Open WhatsApp on your phone.",
            <>
              Go to <b className="font-medium">Settings → Linked devices</b> (Android: <b className="font-medium">⋮ → Linked devices</b>).
            </>,
            <>
              Tap <b className="font-medium">Link a device</b> and point the phone at this code.
            </>,
          ]}
        />
        <p className="tabular text-xs text-muted-foreground">
          {qr?.png && left > 0 ? `The code changes in ${left} s; the new one shows here by itself.` : "Codes change every 20 seconds or so."}
        </p>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={onPhone}>
            <Smartphone /> Use a phone number instead
          </Button>
          <Button variant="ghost" size="sm" onClick={() => void attempt(() => waApi.cancelLink())}>
            Cancel
          </Button>
        </div>
      </div>
    </div>
  );
}

function PhoneStep({ onQR, onBack }: { onQR: () => void; onBack: () => void }) {
  const qr = useWa((s) => s.qr);
  const state = useWa((s) => s.status?.state);
  const [phone, setPhone] = useState("");
  const [busy, setBusy] = useState(false);
  const code = state === "linking" ? qr?.pairing_code : undefined;
  const clean = phone.replace(/[\s()-]/g, "");
  const valid = /^\+\d{8,15}$/.test(clean);

  async function getCode() {
    setBusy(true);
    try {
      const r = await waApi.link("phone", clean);
      if (r?.pairing_code) useWa.setState({ qr: { pairing_code: r.pairing_code } });
    } catch (e) {
      toast.error(sentence((e as Error).message || "no code came back"));
    } finally {
      setBusy(false);
    }
  }

  if (code) {
    return (
      <div className="space-y-4">
        <div className="flex flex-wrap items-center gap-2">
          <span className="rounded-lg border bg-muted/40 px-4 py-2 font-mono text-2xl font-semibold tracking-[0.18em]" aria-label="Pairing code">
            {code}
          </span>
          <CopyButton text={code.replace(/-/g, "")} label="Copy the code" />
        </div>
        <Steps
          items={[
            "Open WhatsApp on the phone with this number.",
            <>
              Go to <b className="font-medium">Linked devices → Link a device</b>.
            </>,
            <>
              Tap <b className="font-medium">Link with phone number instead</b> and type this code.
            </>,
          ]}
        />
        <div className="flex flex-wrap gap-2">
          <Button variant="ghost" size="sm" onClick={() => void attempt(() => waApi.cancelLink())}>
            Cancel
          </Button>
        </div>
      </div>
    );
  }
  return (
    <form
      className="space-y-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid && !busy) void getCode();
      }}
    >
      <p className="text-sm">Your WhatsApp number, with the country code. WhatsApp then shows a code here to type on your phone.</p>
      <div className="flex max-w-sm items-center gap-2">
        <Input
          value={phone}
          onChange={(e) => setPhone(e.target.value)}
          placeholder="+41 79 123 45 67"
          inputMode="tel"
          autoComplete="tel"
          aria-label="Your WhatsApp number"
          autoFocus
          disabled={busy}
        />
        <Button type="submit" size="sm" disabled={!valid || busy}>
          {busy && <Loader2 className="animate-spin" />}
          Get a code
        </Button>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" size="sm" onClick={onQR}>
          <QrCode /> Scan a QR code instead
        </Button>
        {state === "linking" ? (
          <Button type="button" variant="ghost" size="sm" onClick={() => void attempt(() => waApi.cancelLink())}>
            Cancel
          </Button>
        ) : (
          <Button type="button" variant="ghost" size="sm" onClick={onBack}>
            Back
          </Button>
        )}
      </div>
    </form>
  );
}

function intro(s: WaStatus): { title: string; text: string } {
  if (s.state === "logged_out")
    return { title: "WhatsApp logged this machine out", text: "That happens when it's removed on the phone, or after about two weeks offline. Link it again to carry on." };
  if (s.state === "error" && s.error) return { title: "Linking didn't work", text: sentence(s.error) };
  return {
    title: "Link your WhatsApp",
    text: "tmx joins your own account as a linked device, like WhatsApp Web. Your phone stays the main device; chats and messages are kept on this machine.",
  };
}

/** The linking flow: the intro with the warning, then the QR code or the pairing code. Nothing once linked. */
export function WaLinkPanel({ className }: { className?: string }) {
  const s = useWa((x) => x.status);
  const qr = useWa((x) => x.qr);
  const [method, setMethod] = useState<"qr" | "phone" | null>(null);
  const [busy, setBusy] = useState(false);
  const state = s?.state;
  useEffect(() => {
    // after a reload mid-link: the kind of code on its way says which flow it is
    if (state === "linking" && !method) setMethod(qr?.pairing_code ? "phone" : "qr");
    if (state !== "linking" && method === "qr") setMethod(null);
  }, [state, qr?.pairing_code, method]);
  if (!s || isLinked(s)) {
    if (s?.state === "connecting")
      return (
        <div className={cn("flex items-center gap-2.5 text-sm", className)}>
          <Loader2 className="size-4 animate-spin text-muted-foreground" />
          Connecting to WhatsApp…
        </div>
      );
    return null;
  }

  async function startQR() {
    setMethod("qr");
    setBusy(true);
    const ok = await attempt(() => waApi.link("qr"));
    if (!ok) setMethod(null);
    setBusy(false);
  }

  async function toPhone() {
    if (state === "linking") await attempt(() => waApi.cancelLink());
    useWa.setState({ qr: null });
    setMethod("phone");
  }

  async function toQR() {
    if (state === "linking") await attempt(() => waApi.cancelLink());
    await startQR();
  }

  if (method === "phone")
    return (
      <div className={className}>
        <PhoneStep onQR={() => void toQR()} onBack={() => setMethod(null)} />
      </div>
    );
  if (state === "linking")
    return (
      <div className={className}>
        <QRStep onPhone={() => void toPhone()} />
      </div>
    );

  const t = intro(s);
  return (
    <div className={cn("space-y-3", className)}>
      <div className="flex items-start gap-2.5">
        <span className={cn("mt-0.5 shrink-0", s.state === "unlinked" ? "text-muted-foreground" : "text-state-waiting")}>
          {s.state === "unlinked" ? <Smartphone className="size-4" /> : <TriangleAlert className="size-4" />}
        </span>
        <div className="min-w-0">
          <p className="text-sm font-medium">{t.title}</p>
          <p className="mt-0.5 text-sm text-muted-foreground">{t.text}</p>
        </div>
      </div>
      <p className="flex items-start gap-2.5 rounded-lg border border-state-waiting/35 bg-state-waiting/[0.07] px-3 py-2 text-xs text-foreground/85" role="note">
        <TriangleAlert className="mt-px size-3.5 shrink-0 text-[color-mix(in_oklch,var(--state-waiting)_80%,var(--foreground))]" />
        {WA_RISK}
      </p>
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={() => void startQR()} disabled={busy}>
          {busy ? <Loader2 className="animate-spin" /> : s.state === "unlinked" ? <QrCode /> : <RefreshCw />}
          {s.state === "unlinked" ? "Link with a QR code" : "Link again"}
        </Button>
        <Button size="sm" variant="ghost" onClick={() => setMethod("phone")} disabled={busy}>
          <Smartphone /> Use a phone number
        </Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------------------------
// settings

function chatName(chats: ReturnType<typeof useWa.getState>["chats"], jid: string): string {
  return chats[jid]?.name || waPhone(jid) || jid.split("@")[0];
}

/** Chats in one of the per-chat lists, as chips, with "Add a chat". */
function ChatList({ value, onChange, empty, addLabel }: { value: string[]; onChange: (next: string[]) => void; empty: string; addLabel: string }) {
  const chats = useWa((s) => s.chats);
  const [open, setOpen] = useState(false);
  const options = useMemo(() => [...sortedWaChats(chats), ...sortedWaChats(chats, { archived: true })].filter((c) => !value.includes(c.jid)), [chats, value]);
  return (
    <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
      {value.length === 0 && <span className="mr-1 text-xs text-subtle-foreground">{empty}</span>}
      {value.map((jid) => {
        const c = chats[jid];
        return (
          <span key={jid} className="inline-flex h-7 max-w-60 items-center gap-1.5 rounded-full border bg-background pr-1 pl-1 text-xs">
            <WaAvatar chat={c ?? { kind: jid.endsWith("@g.us") ? "group" : "dm", name: chatName(chats, jid) }} size="sm" />
            <span className="min-w-0 truncate">{chatName(chats, jid)}</span>
            <button
              type="button"
              aria-label={`Remove ${chatName(chats, jid)}`}
              onClick={() => onChange(value.filter((x) => x !== jid))}
              className="flex size-5 items-center justify-center rounded-full text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
            >
              <X className="size-3" />
            </button>
          </span>
        );
      })}
      <Button variant="ghost" size="xs" className="text-muted-foreground" onClick={() => setOpen(true)} disabled={options.length === 0}>
        <Plus /> {addLabel}
      </Button>
      <CommandDialog open={open} onOpenChange={setOpen} title={addLabel} description="Pick a WhatsApp chat" className="sm:max-w-[27.5rem]" showCloseButton={false}>
        <CommandInput placeholder="Search chats…" />
        <CommandList className="scroll-thin">
          <CommandEmpty>No chat matches.</CommandEmpty>
          <CommandGroup>
            {options.map((c) => (
              <CommandItem
                key={c.jid}
                value={`${c.name} ${c.jid}`}
                onSelect={() => {
                  onChange([...value, c.jid]);
                  setOpen(false);
                }}
              >
                <WaAvatar chat={c} size="sm" />
                <span className="truncate">{c.name}</span>
                <span className="ml-auto text-xs text-muted-foreground">{c.kind === "group" ? "group" : waPhone(c.jid)}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        </CommandList>
      </CommandDialog>
    </div>
  );
}

function StatusLine({ s }: { s: WaStatus }) {
  const since = s.linked_at ? new Date(s.linked_at).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }) : "";
  if (s.state === "connecting") {
    return (
      <div className="flex items-start gap-2.5 text-sm">
        <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin text-muted-foreground" />
        <div>
          <p>Connecting to WhatsApp…</p>
          <p className="mt-0.5 text-xs text-muted-foreground">New messages arrive as soon as it's back. Your phone needn't be on.</p>
        </div>
      </div>
    );
  }
  if (s.state === "error") {
    return (
      <div className="flex items-start gap-2.5 text-sm text-state-waiting">
        <TriangleAlert className="mt-0.5 size-4 shrink-0" />
        <div className="min-w-0">
          <p className="[overflow-wrap:anywhere]">{s.error ? sentence(s.error) : "WhatsApp isn't reachable."}</p>
          <p className="mt-0.5 text-xs text-muted-foreground">tmx keeps trying by itself.</p>
        </div>
      </div>
    );
  }
  return (
    <div className="flex items-start gap-2.5 text-sm">
      <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-state-ok" />
      <div className="min-w-0 flex-1">
        <p>
          Linked{s.name ? ` as ${s.name}` : ""}
          {s.phone && <span className="font-mono text-[0.7812rem] text-muted-foreground"> · {s.phone}</span>}
        </p>
        <p className="mt-0.5 text-xs text-muted-foreground">
          {since ? `Since ${since}. ` : ""}This machine is one of your linked devices; you can also remove it on your phone.
        </p>
        {s.history?.syncing && (
          <div className="mt-2 space-y-1">
            <p className="text-xs text-muted-foreground">Bringing in your chats from the phone…</p>
            <SyncBar progress={s.history.progress} />
          </div>
        )}
      </div>
    </div>
  );
}

function UnlinkRow() {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  return (
    <Row label="Unlink" hint="Logs tmx out of your WhatsApp. Your phone and its chats stay as they are.">
      <Button variant="outline" size="sm" className="text-destructive hover:text-destructive" disabled={busy} onClick={() => setOpen(true)}>
        {busy ? <Loader2 className="animate-spin" /> : <Unlink />}
        Unlink…
      </Button>
      <AlertDialog open={open} onOpenChange={setOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Unlink WhatsApp from this machine?</AlertDialogTitle>
            <AlertDialogDescription>
              tmx stops receiving and sending WhatsApp messages, and your agents can no longer read them. You can link again at any time with a new code.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={async () => {
                setBusy(true);
                await attempt(() => waApi.unlink(), "Unlinked. This machine is no longer one of your WhatsApp devices.");
                setBusy(false);
              }}
            >
              Unlink
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Row>
  );
}

function SettingsRows({ settings }: { settings: WaSettings }) {
  const save = (next: Partial<WaSettings>) => void attempt(() => saveWaSettings({ ...settings, ...next }));
  return (
    <>
      <Row label="Read receipts" hint="On: reading a chat in tmx shows the sender blue ticks. Off: only your phone sends them.">
        <Switch checked={settings.read_receipts} onCheckedChange={(v) => save({ read_receipts: v })} aria-label="Send read receipts" className="mt-1" />
      </Row>
      <Row label="When an agent writes" hint="Agents send one message at a time, spaced out, and never to many chats at once.">
        <div className="space-y-2">
          <Select value={settings.agents_send} onValueChange={(v) => save({ agents_send: v as WaSettings["agents_send"] })}>
            <SelectTrigger className="w-full sm:w-72" aria-label="When an agent writes">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="ask">Ask me first</SelectItem>
              <SelectItem value="allowed">Send without asking</SelectItem>
            </SelectContent>
          </Select>
          {settings.agents_send === "allowed" ? (
            <p className="text-xs text-state-waiting">Agents can write in any chat without asking you.</p>
          ) : (
            <p className="text-xs text-muted-foreground">Each message waits in Needs you for your Allow, except in the chats below.</p>
          )}
        </div>
      </Row>
      {settings.agents_send === "ask" && (
        <Row label="Agents may send without asking in" hint="Only these chats skip your Allow.">
          <ChatList value={settings.allowed_chats ?? []} onChange={(v) => save({ allowed_chats: v })} empty="No chats." addLabel="Add a chat" />
        </Row>
      )}
      <Row label="Tell the head about new messages in" hint="The head gets a short note for each new message there, as information from outside, never as instructions.">
        <ChatList value={settings.notify_head_chats ?? []} onChange={(v) => save({ notify_head_chats: v })} empty="No chats." addLabel="Add a chat" />
      </Row>
    </>
  );
}

export function WhatsAppSection() {
  const status = useStore((x) => x.status);
  const available = useWa((x) => x.available);
  const off = useWa((x) => x.offMessage);
  const s = useWa((x) => x.status);

  useEffect(() => {
    if (status === "ready") void loadWhatsApp();
  }, [status]);

  if (available === false) return null; // a daemon without WhatsApp
  return (
    <div id="whatsapp" className="scroll-mt-6">
      <Section title="WhatsApp">
        <div className="divide-y rounded-xl border bg-card px-4">
          {off ? (
            <div className="flex items-start gap-2.5 py-3.5 text-sm">
              <TriangleAlert className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
              <div className="min-w-0">
                <p>WhatsApp is off on this tmx.</p>
                <p className="mt-0.5 text-xs [overflow-wrap:anywhere] text-muted-foreground">{sentence(off)}</p>
              </div>
            </div>
          ) : !s ? (
            <div className="flex items-center gap-2 py-3.5 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" /> Loading…
            </div>
          ) : isLinked(s) ? (
            <>
              <div className="py-3.5">
                <StatusLine s={s} />
              </div>
              <SettingsRows settings={s.settings} />
              <UnlinkRow />
            </>
          ) : (
            <div className="py-4">
              <WaLinkPanel />
            </div>
          )}
        </div>
      </Section>
    </div>
  );
}
