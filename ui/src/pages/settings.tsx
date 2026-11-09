import { useEffect, useState } from "react";
import { ProcessList } from "@/components/process-list";
import { ExternalLink, Laptop, Moon, Sun } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import { CopyButton } from "@/components/copy-button";
import { DiscoverSection } from "@/components/discover-section";
import { PageBody, PageHeader, PageTitle, Section } from "@/components/page";
import { P2PSection } from "@/components/p2p-settings";
import { ProfilesSection } from "@/components/profiles-section";
import { SafetySection } from "@/components/safety-settings";
import { PhoneNotifications } from "@/components/phone-notifications";
import { OwnerDevices } from "@/components/device-pairing";
import { CompanionSection } from "@/components/companion/settings";
import { UpdateSection } from "@/components/update-settings";
import { VoiceSection } from "@/components/voice-settings";
import { RecordingSettingsSection } from "@/components/recording-settings";
import { WhatsAppSection } from "@/components/whatsapp/wa-settings";
import { ResourceSettings } from "@/components/resources/resource-settings";
import { Truncate } from "@/components/truncate";
import { displayPath, modelName } from "@/lib/format";
import { api } from "@/lib/api";
import { useStore } from "@/lib/store";
import { setTheme, useTheme, type ThemePref } from "@/lib/theme";
import { applyTextSize, savedTextSize, TEXT_SIZES, type TextSize } from "@/lib/text-size";
import { cn } from "@/lib/utils";
import { chatApi } from "@/lib/chats";
import { toast } from "sonner";

export const KNOWN_MODELS = ["claude-opus-5-5", "claude-sonnet-5", "claude-haiku-4-5-20251001"];

export const MODES: { value: string; label: string; hint: string }[] = [
  { value: "auto", label: "Auto", hint: "Claude's classifier decides; anything it escalates comes to Needs you." },
  { value: "default", label: "Ask", hint: "Every tool that needs permission comes to Needs you." },
  { value: "acceptEdits", label: "Accept edits", hint: "File edits are allowed; commands still ask." },
  { value: "plan", label: "Plan only", hint: "The agent plans and reads but changes nothing." },
  { value: "dontAsk", label: "Don't ask", hint: "Anything not explicitly allowed is refused without asking you." },
  { value: "bypassPermissions", label: "Bypass permissions", hint: "Nothing asks. The agent can run any command." },
];

export function Row({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
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

export function ModelPicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const known = KNOWN_MODELS.includes(value);
  const [custom, setCustom] = useState(!known);
  return (
    <div className="flex flex-col gap-2 sm:flex-row">
      <Select
        value={custom ? "__custom" : value}
        onValueChange={(v) => {
          if (v === "__custom") setCustom(true);
          else {
            setCustom(false);
            onChange(v);
          }
        }}
      >
        <SelectTrigger className="w-full sm:w-72">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {KNOWN_MODELS.map((m) => (
            <SelectItem key={m} value={m}>
              <span>{modelName(m)}</span>
              <span className="font-mono text-2xs text-muted-foreground">{m}</span>
            </SelectItem>
          ))}
          <SelectSeparator />
          <SelectItem value="__custom">Other model ID…</SelectItem>
        </SelectContent>
      </Select>
      {custom && (
        <Input value={value} onChange={(e) => onChange(e.target.value.trim())} placeholder="claude-…" className="font-mono text-[0.7812rem] sm:w-64" spellCheck={false} autoFocus />
      )}
    </div>
  );
}

function ThemePicker() {
  const { pref } = useTheme();
  const opts: { v: ThemePref; label: string; icon: React.ComponentType<{ className?: string }> }[] = [
    { v: "system", label: "System", icon: Laptop },
    { v: "light", label: "Light", icon: Sun },
    { v: "dark", label: "Dark", icon: Moon },
  ];
  return (
    <div role="radiogroup" aria-label="Theme" className="inline-flex rounded-lg border bg-muted/40 p-0.5">
      {opts.map((o) => (
        <button
          key={o.v}
          role="radio"
          aria-checked={pref === o.v}
          onClick={() => setTheme(o.v)}
          className={cn(
            "flex h-7 items-center gap-1.5 rounded-md px-3 text-[0.7812rem] text-muted-foreground outline-none transition-colors duration-150 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring",
            pref === o.v && "bg-background font-medium text-foreground shadow-sm dark:bg-accent",
          )}
        >
          <o.icon className="size-3.5" />
          {o.label}
        </button>
      ))}
    </div>
  );
}

/** Text size, per device (lib/text-size.ts): the whole UI scales with it. */
function TextSizePicker() {
  const [size, setSize] = useState<TextSize>(savedTextSize);
  return (
    <div role="radiogroup" aria-label="Text size" className="inline-flex rounded-lg border bg-muted/40 p-0.5">
      {TEXT_SIZES.map((o) => (
        <button
          key={o.id}
          role="radio"
          aria-checked={size === o.id}
          onClick={() => {
            applyTextSize(o.id);
            setSize(o.id);
          }}
          className={cn(
            "flex h-7 items-center rounded-md px-3 text-[0.7812rem] text-muted-foreground outline-none transition-colors duration-150 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring",
            size === o.id && "bg-background font-medium text-foreground shadow-sm dark:bg-accent",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

/** The name other people see in invites and chats (PUT /api/p2p/name). */
function ChatNameRow() {
  const [saved, setSaved] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [off, setOff] = useState(false);
  useEffect(() => {
    chatApi.p2p().then(
      (s) => {
        setOff(!s.enabled);
        setSaved(s.name ?? "");
        setName(s.name ?? "");
      },
      () => setOff(true),
    );
  }, []);
  if (off) return null;
  const clean = name.trim().replace(/\s+/g, " ");
  const dirty = saved !== null && clean !== saved && clean.length > 0;
  async function save() {
    setBusy(true);
    try {
      const r = await chatApi.setName(clean);
      setSaved(r.name);
      setName(r.name);
      toast.success("Saved. New invites and messages use this name.");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Row label="Name in chats" hint="What other people see in your invites and messages. People who already have you as a contact keep the name they gave you.">
      <form
        className="flex max-w-sm items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (dirty) void save();
        }}
      >
        <Input value={name} maxLength={60} onChange={(e) => setName(e.target.value)} disabled={saved === null || busy} aria-label="Name in chats" />
        <Button type="submit" size="sm" variant={dirty ? "default" : "outline"} disabled={!dirty || busy}>
          Save
        </Button>
      </form>
    </Row>
  );
}

function HeadHarnessSection() {
  const [value, setValue] = useState<string>("claude");
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api.getConfig().then((c) => {
      setValue(c.head_harness || "claude");
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);
  async function onChange(v: string) {
    const prev = value;
    setValue(v);
    setBusy(true);
    try {
      await api.putConfig({ head_harness: v }); // owner gate: may ask for a confirmation first
      toast.success("Saved. Restart the daemon (tmx down && tmx up) for this to take effect.");
    } catch (e) {
      setValue(prev); // not saved (refused, or the confirmation was cancelled)
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Section title="Head agent">
      <div className="rounded-xl border bg-card px-4 py-1">
        <Row label="Head harness" hint="The harness for the head agent (the top of the tree). Chat-capable harnesses only. Takes effect after a daemon restart.">
          <Select value={value} onValueChange={onChange} disabled={!loaded || busy}>
            <SelectTrigger className="w-full sm:w-72">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="claude">Claude Code</SelectItem>
              <SelectItem value="codex">Codex</SelectItem>
            </SelectContent>
          </Select>
        </Row>
      </div>
    </Section>
  );
}

const SETTINGS_SECTIONS = [
  ["phone-notifications", "Phone notifications"], ["system-tools", "System tools"],
  ["project-tools", "Project tools"], ["profiles", "Profiles"], ["head-agent", "Head agent"],
  ["safety", "Safety"], ["owner-devices", "Owner devices"], ["companion", "Companion"],
  ["peer-to-peer", "Peer-to-peer"], ["resources", "Resources"], ["harnesses", "Harnesses"],
  ["running-outside-tmx", "Running outside tmx"], ["updates", "Updates"], ["voice", "Voice"],
  ["recordings", "Recordings"], ["whatsapp", "WhatsApp"], ["appearance", "Appearance"],
  ["this-machine", "This machine"],
];

export function SettingsPage() {
  const [section, setSection] = useState(() => new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("section") || "phone-notifications");
  const workspace = useStore((s) => s.workspace);
  const machine = useStore((s) => s.machine);
  const version = useStore((s) => s.version);
  const transport = useStore((s) => s.transport);

  useEffect(() => {
    // #/settings?section=companion scrolls to that section.
    const section = new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("section");
    if (section) setTimeout(() => document.getElementById(section)?.scrollIntoView({ block: "start" }), 50);
  }, []);

  const browserURL = transport ? `${transport.baseURL}/#token=${encodeURIComponent(transport.token)}` : "";
  const via = transport?.kind === "mock" ? "Mock data" : transport?.kind === "bridge" ? "Desktop bridge" : transport?.desktop ? "Direct (desktop)" : "Direct";

  return (
    <div className="settings-page flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader>
        <PageTitle>Settings</PageTitle>
      </PageHeader>
      <nav aria-label="Settings sections" className="shrink-0 border-b px-4 py-2 md:hidden">
        <label htmlFor="settings-section" className="sr-only">Settings section</label>
        <select id="settings-section" value={section} className="min-h-11 w-full min-w-0 rounded-md border bg-background px-3 text-[18px]" onChange={e => {
          setSection(e.target.value);
          document.getElementById(e.target.value)?.scrollIntoView({ block: "start" });
        }}>
          {SETTINGS_SECTIONS.map(([id, label]) => <option key={id} value={id}>{label}</option>)}
        </select>
      </nav>
      <PageBody width="max-w-[51.25rem]" className="space-y-10">
        <PhoneNotifications />
        <ProcessList />
        <ProcessList projectTools />
        <ProfilesSection />

        <HeadHarnessSection />

        <SafetySection />
        <OwnerDevices />

        <div id="companion" className="scroll-mt-6">
          <CompanionSection />
        </div>

        <P2PSection />

        <ResourceSettings />
        <DiscoverSection />

        <UpdateSection />

        <VoiceSection />

        <RecordingSettingsSection />
        <WhatsAppSection />

        <Section title="Appearance">
          <div className="rounded-xl border bg-card px-4 py-1">
            <Row label="Theme" hint="System follows your OS setting.">
              <ThemePicker />
            </Row>
            <Row label="Text size" hint="For this device. Every page grows with it; no zooming needed.">
              <TextSizePicker />
            </Row>
          </div>
        </Section>

        <Section title="This machine">
          <div className="divide-y rounded-xl border bg-card px-4">
            <Row label="Machine">
              <p className="pt-1.5 text-sm">{machine}</p>
            </Row>
            <ChatNameRow />
            <Row label="Workspace" hint="New project folders go here.">
              <div className="flex min-w-0 items-center gap-1 pt-1">
                <Truncate className="font-mono text-[0.7812rem]" title={workspace}>
                  {displayPath(workspace)}
                </Truncate>
                <CopyButton text={workspace} label="Copy path" />
              </div>
            </Row>
            <Row label="Open in a browser" hint="The same UI, served by the daemon. The link carries your owner token: keep it to yourself.">
              <div className="flex flex-wrap items-center gap-1.5">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => transport?.openExternal(browserURL)}
                  disabled={!transport || transport.kind === "mock"}
                >
                  <ExternalLink />
                  Open in browser
                </Button>
                <CopyButton text={browserURL} label="Copy link" />
                <span className="font-mono text-2xs text-subtle-foreground">{transport?.baseURL}</span>
              </div>
            </Row>
            <Row label="Connection">
              <p className="pt-1.5 text-sm">{via}</p>
            </Row>
            <Row label="Version">
              <p className="pt-1.5 font-mono text-[0.7812rem]">{version}</p>
            </Row>
          </div>
        </Section>
      </PageBody>
    </div>
  );
}
