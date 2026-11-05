// Settings → Companion (DESIGN.md "Companion"): both links with clear next steps, and the tasks
// tmx handed to Companion.
//   Companion reads tmx: tmx's MCP entry + skill in Companion's folder (~/.aios), a token of its own.
//   tmx hands Companion tasks: the agent key Companion issued to tmx, Companion's address.
//   One click connects it: Connect with Companion opens Companion's "Connect tmx?", and Companion
//   hands tmx its key itself; pasting a key stays as a quiet fallback.
import { useEffect, useState } from "react";
import { BookOpen, Check, CircleAlert, CircleDashed, Eye, KeyRound, Loader2, Plug, RefreshCw, Send, Sparkles, Unplug } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Section } from "@/components/page";
import { displayPath, relativeTime } from "@/lib/format";
import { companionApi, companionChat, loadCompanionStatus, sortedTasks, useCompanion, type CompanionStatus } from "@/lib/companion";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";
import { TaskRow } from "./bits";
import { openHandOff } from "./hand-off-dialog";

type Tone = "ok" | "todo" | "warn";

function Mark({ tone }: { tone: Tone }) {
  if (tone === "ok")
    return (
      <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-state-ok/15">
        <Check className="size-3 text-state-ok" strokeWidth={3} />
      </span>
    );
  if (tone === "warn") return <CircleAlert className="size-5 shrink-0 text-state-error" />;
  return <CircleDashed className="size-5 shrink-0 text-subtle-foreground" />;
}

function Step({ tone, title, children, actions }: { tone: Tone; title: React.ReactNode; children?: React.ReactNode; actions?: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-3 px-4 py-3.5 sm:flex-row sm:items-start">
      <div className="flex min-w-0 flex-1 gap-3">
        <Mark tone={tone} />
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium">{title}</div>
          {children && <div className="mt-0.5 space-y-1.5 text-xs text-muted-foreground">{children}</div>}
        </div>
      </div>
      {actions && <div className="flex shrink-0 flex-wrap items-center gap-1.5 pl-8 sm:pl-0">{actions}</div>}
    </div>
  );
}

const SOURCE: Record<string, string> = {
  config: "set by you",
  "engine.json": "found in Companion's engine.json",
  "engine-state": "found in Companion's engine state",
  default: "Companion's default port",
};

function ReadLink({ st, onChange }: { st: CompanionStatus; onChange: () => void }) {
  const [busy, setBusy] = useState("");
  const i = st.install;
  async function act(what: "install" | "uninstall" | "rotate") {
    setBusy(what);
    try {
      if (what === "install") await companionApi.install();
      if (what === "uninstall") await companionApi.uninstall();
      if (what === "rotate") await companionApi.rotateLinkToken();
      toast.success(
        { install: "Installed into Companion", uninstall: "Removed from Companion", rotate: "New token for Companion" }[what],
        {
          description: {
            install: "Companion picks it up on its next start, or when you reload its MCP servers.",
            uninstall: "Its token no longer works.",
            rotate: "The old one stopped working; Companion's entry reads the new one by itself.",
          }[what],
        },
      );
      onChange();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy("");
    }
  }
  const tone: Tone = i.foreign || i.skill_foreign ? "warn" : i.installed && i.current && st.link_token ? "ok" : "todo";
  let line: React.ReactNode;
  if (!i.companion_dir) line = <>Companion has not been set up for this user here ({displayPath(i.aios_home)} is missing). Open Companion once, then install.</>;
  else if (i.foreign) line = <>Companion already has an entry named “tmx” that tmx did not make ({displayPath(i.mcp_path)}). tmx leaves it alone; remove it in Companion first.</>;
  else if (i.skill_foreign) line = <>Companion already has a skill named “tmx” that tmx did not make. tmx leaves it alone.</>;
  else if (!i.installed)
    line = (
      <>
        Adds tmx to Companion's MCP servers and a short skill. Companion can then see your agents, what needs you and your chats, and pass a message to the head.
        {!st.connected && " Connect with Companion below does this too."}
      </>
    );
  else if (!i.current) line = <>Installed for another tmx binary or address: install again to update it.</>;
  else if (!st.link_token) line = <>Installed, but its token is missing: install again.</>;
  else
    line = (
      <>
        Installed in {displayPath(i.mcp_path)}
        {i.enabled ? "" : " (switched off in Companion)"}.{" "}
        {st.link_used_at ? `Companion last read tmx ${relativeTime(st.link_used_at)}.` : "Companion has not used it since tmx started."}
      </>
    );
  return (
    <Step
      tone={tone}
      title={
        <span className="flex items-center gap-1.5">
          <Eye className="size-3.5 text-muted-foreground" />
          Companion reads tmx
        </span>
      }
      actions={
        i.companion_dir && !i.foreign && !i.skill_foreign ? (
          i.installed ? (
            <>
              {(!i.current || !st.link_token) && (
                <Button size="sm" onClick={() => void act("install")} disabled={!!busy}>
                  {busy === "install" && <Loader2 className="animate-spin" />}
                  Install again
                </Button>
              )}
              <Button size="sm" variant="ghost" className="text-muted-foreground" onClick={() => void act("rotate")} disabled={!!busy} title="New token: the old one stops working at once">
                <KeyRound />
                New token
              </Button>
              <Button size="sm" variant="outline" onClick={() => void act("uninstall")} disabled={!!busy}>
                {busy === "uninstall" && <Loader2 className="animate-spin" />}
                Remove
              </Button>
            </>
          ) : (
            <Button size="sm" variant={st.connected ? "default" : "outline"} onClick={() => void act("install")} disabled={!!busy}>
              {busy === "install" ? <Loader2 className="animate-spin" /> : <Sparkles />}
              Install into Companion
            </Button>
          )
        ) : undefined
      }
    >
      <p>{line}</p>
      <p className="text-subtle-foreground">It cannot approve, start, stop or change anything in tmx, and everything it reads is marked as data, not instructions.</p>
    </Step>
  );
}

/** How long Connect with Companion waits for the key, how often it looks, and when it starts
 * hinting that Companion may not have opened. */
const WAIT_MS = 120_000;
const POLL_MS = 2000;
const SLOW_MS = 15_000;

/** Connect with Companion: the daemon installs tmx into Companion when needed and opens
 * Companion's "Connect tmx?" on this machine (POST /api/companion/open-connect); Companion
 * hands tmx its key when the owner clicks Connect there. Waits (polling the status) until a new
 * key is stored (key_set_at changes), for up to 2 minutes. */
function useConnectWithCompanion(st: CompanionStatus, onChange: () => void) {
  const [waiting, setWaiting] = useState<{ since: number; before: number } | null>(null);
  const [slow, setSlow] = useState(false);
  const [opening, setOpening] = useState(false);
  const [problem, setProblem] = useState("");

  async function start() {
    setOpening(true);
    setProblem("");
    try {
      const before = st.key_set_at ?? 0;
      const r = await companionApi.openConnect();
      if (r.installed) onChange();
      if (!r.opened) {
        setProblem(r.error || "Companion did not open.");
        return;
      }
      setSlow(false);
      setWaiting({ since: Date.now(), before });
    } catch (e) {
      setProblem((e as Error).message);
    } finally {
      setOpening(false);
    }
  }

  useEffect(() => {
    if (!waiting) return;
    let stopped = false;
    let timer = 0;
    const tick = async () => {
      try {
        const s = await companionApi.status(true);
        if (stopped) return;
        if (s.connected && (s.key_set_at ?? 0) !== waiting.before) {
          setWaiting(null);
          companionChat.refreshSoon(0);
          if (s.key_ok === false) setProblem(`Companion gave tmx a key, but does not accept it now${s.error ? ` (${s.error})` : ""}. Try again.`);
          else toast.success("Connected to Companion", { description: `Companion knows tmx as “${s.label || "tmx"}”. Waiting tasks go out now.` });
          return;
        }
      } catch {
        /* the daemon restarting: keep waiting */
      }
      if (stopped) return;
      const waited = Date.now() - waiting.since;
      if (waited > WAIT_MS) {
        setWaiting(null);
        setProblem("Companion has not given tmx a key yet. Click Connect with Companion to ask again, or paste a key instead.");
        return;
      }
      setSlow(waited > SLOW_MS);
      timer = window.setTimeout(() => void tick(), POLL_MS);
    };
    timer = window.setTimeout(() => void tick(), POLL_MS);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [waiting]);

  return { waiting: !!waiting, slow, opening, problem, start, stop: () => setWaiting(null) };
}

function TaskLink({ st, onChange }: { st: CompanionStatus; onChange: () => void }) {
  const [token, setToken] = useState("");
  const [url, setUrl] = useState("");
  const [showUrl, setShowUrl] = useState(false);
  const [paste, setPaste] = useState(false);
  const [busy, setBusy] = useState("");
  const checking = useCompanion((s) => s.checking);
  const one = useConnectWithCompanion(st, onChange);

  async function connect(e: React.FormEvent) {
    e.preventDefault();
    if (!token.trim()) return;
    setBusy("connect");
    try {
      const s = await companionApi.connect(token.trim(), url.trim());
      setToken("");
      setPaste(false);
      one.stop();
      if (s.key_ok) toast.success("Connected to Companion", { description: s.label ? `Companion knows tmx as “${s.label}”. Waiting tasks go out now.` : undefined });
      else toast.warning("Key saved, not checked yet", { description: s.error || "Companion is not running right now; tmx checks the key when it sends a task." });
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setBusy("");
    }
  }
  async function disconnect() {
    setBusy("disconnect");
    try {
      await companionApi.disconnect();
      toast("tmx forgot Companion's key", { description: "Remove tmx in Companion too (People & Agents)." });
      onChange();
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setBusy("");
    }
  }

  const tone: Tone = !st.connected ? "todo" : st.checked && (!st.reachable || st.key_ok === false) ? "warn" : st.key_ok ? "ok" : "todo";
  const refused = st.connected && st.key_ok === false;
  const needsKey = !st.connected || refused;
  const notRunning = st.checked && !st.reachable;
  const where = `${st.address.url}, ${SOURCE[st.address.source] ?? st.address.source}`;
  const i = st.install;

  const connectButton = (
    <Button size="sm" onClick={() => void one.start()} disabled={one.opening || one.waiting || !!busy} data-connect-companion>
      {one.opening || one.waiting ? <Loader2 className="animate-spin" /> : <Plug />}
      {one.waiting ? "Waiting for Companion" : "Connect with Companion"}
    </Button>
  );
  const keyForm = (
    <>
      <form onSubmit={connect} className="flex flex-col gap-1.5 pt-1 sm:flex-row">
        <Input
          type="password"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder="Companion's key for tmx"
          className="h-8 font-mono text-[0.7812rem] sm:max-w-72"
          autoComplete="off"
          spellCheck={false}
          aria-label="Companion's key for tmx"
          autoFocus
        />
        {showUrl && (
          <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://127.0.0.1:8010" className="h-8 font-mono text-[0.7812rem] sm:max-w-52" aria-label="Companion's address" spellCheck={false} />
        )}
        <Button type="submit" size="sm" variant="outline" className="h-8" disabled={!token.trim() || !!busy}>
          {busy === "connect" && <Loader2 className="animate-spin" />}
          Save key
        </Button>
        <Button type="button" size="sm" variant="ghost" className="h-8 text-muted-foreground" onClick={() => setPaste(false)}>
          Cancel
        </Button>
      </form>
      {!showUrl && (
        <button type="button" className="self-start text-left text-2xs text-subtle-foreground hover:text-foreground hover:underline" onClick={() => setShowUrl(true)}>
          Companion at another address? (now {where})
        </button>
      )}
    </>
  );
  // The one-click flow's progress and problems, and the quiet fallback: paste a key.
  const connecting = (
    <>
      {one.waiting && (
        <div className="flex items-center gap-2 rounded-lg bg-muted/70 px-2.5 py-2 text-[0.7812rem] text-foreground/85" role="status" data-companion-connecting>
          <Loader2 className="size-3.5 shrink-0 animate-spin text-brand" />
          <span className="min-w-0 flex-1">
            Companion is open: click <span className="font-medium text-foreground">Connect</span> next to tmx.
          </span>
          <button type="button" className="shrink-0 text-2xs text-muted-foreground hover:text-foreground hover:underline" onClick={one.stop}>
            Cancel
          </button>
        </div>
      )}
      {one.waiting && one.slow && (
        <p>
          {notRunning
            ? "Companion isn't answering yet. If it didn't open, start Companion yourself, open People & Agents and click Connect next to tmx."
            : "Don't see “Connect tmx?” in Companion? Open People & Agents there and click Connect next to tmx, or update Companion."}
        </p>
      )}
      {one.problem && !one.waiting && (
        <p className="flex items-start gap-1.5 text-state-error" role="alert">
          <CircleAlert className="mt-px size-3.5 shrink-0" />
          <span>{one.problem}</span>
        </p>
      )}
      {(needsKey || one.waiting || one.problem || paste) &&
        (paste ? (
          keyForm
        ) : (
          <button type="button" className="self-start text-left text-2xs text-subtle-foreground hover:text-foreground hover:underline" onClick={() => setPaste(true)}>
            Paste a key instead
          </button>
        ))}
    </>
  );
  const tasksNote = (
    <p className="text-subtle-foreground">You see the exact text of every task before it is sent; agents can ask only when their profile allows it, and you allow each one.</p>
  );
  return (
    <Step
      tone={tone}
      title={
        <span className="flex items-center gap-1.5">
          <Send className="size-3.5 text-muted-foreground" />
          tmx hands Companion tasks
        </span>
      }
      actions={
        st.connected ? (
          <>
            <Button size="sm" variant="outline" onClick={() => void loadCompanionStatus(true).then(onChange)} disabled={checking || !!busy}>
              {checking ? <Loader2 className="animate-spin" /> : <RefreshCw />}
              Test connection
            </Button>
            {!refused && (
              <Button
                size="sm"
                variant="ghost"
                className="text-muted-foreground"
                onClick={() => void one.start()}
                disabled={one.opening || one.waiting || !!busy}
                title="Companion makes a new key for tmx; the old one stops working"
              >
                <KeyRound />
                Connect again
              </Button>
            )}
            <Button size="sm" variant="ghost" className="text-muted-foreground" onClick={() => void disconnect()} disabled={!!busy}>
              <Unplug />
              Disconnect
            </Button>
          </>
        ) : (
          connectButton
        )
      }
    >
      {!st.connected ? (
        <>
          {!i.companion_dir ? (
            <p>
              Companion is not set up on this computer yet ({displayPath(i.aios_home)} is missing). Install Companion and open it once, then click Connect with Companion.
            </p>
          ) : (
            <p>
              Companion asks you to confirm, then gives tmx a key of its own; there is nothing to copy.
              {notRunning && " Companion isn't running: Connect with Companion opens it."}
            </p>
          )}
          {connecting}
        </>
      ) : (
        <>
          {!st.checked ? (
            <p>Key saved. Companion is looked for at {where}.</p>
          ) : refused ? (
            <>
              <p>
                Companion {st.version} answers at <span className="font-mono text-[0.7188rem]">{st.address.url}</span> but no longer accepts tmx's key: it was replaced or
                removed in Companion, or Companion is signed in to another account. Tasks wait until you connect again.
              </p>
              {st.error && <p className="text-subtle-foreground">Companion says: {st.error}</p>}
              <div className="pt-1">{connectButton}</div>
            </>
          ) : st.reachable ? (
            <p>
              <span className="font-medium text-state-ok">Connected.</span> Companion {st.version} answers at <span className="font-mono text-[0.7188rem]">{st.address.url}</span> (
              {SOURCE[st.address.source] ?? st.address.source}); it knows tmx as “{st.label || "tmx"}”.
            </p>
          ) : (
            <p>
              Companion does not answer at <span className="font-mono text-[0.7188rem]">{st.address.url}</span> ({SOURCE[st.address.source] ?? st.address.source}). Is it running?
              Tasks wait until it is.
            </p>
          )}
          {connecting}
          {tasksNote}
        </>
      )}
    </Step>
  );
}

export function CompanionSection() {
  const st = useCompanion((s) => s.status);
  const tasks = useCompanion((s) => s.tasks);
  const checking = useCompanion((s) => s.checking);
  const transport = useStore((s) => s.transport);
  const [failed, setFailed] = useState(false);
  const refresh = () => void loadCompanionStatus(false).catch(() => setFailed(true));
  useEffect(() => {
    if (!transport) return;
    void loadCompanionStatus(true).catch(() => setFailed(true));
  }, [transport]);
  const list = sortedTasks(tasks);

  return (
    <Section
      title={
        <span className="flex items-center gap-1.5">
          <Sparkles className="size-3.5 text-brand" />
          Companion
        </span>
      }
      right={
        st && (
          <Button size="xs" variant="ghost" className="text-muted-foreground" onClick={() => void loadCompanionStatus(true)} disabled={checking}>
            {checking ? <Loader2 className="animate-spin" /> : <RefreshCw />}
            Check again
          </Button>
        )
      }
    >
      <p className="-mt-1 mb-3 text-xs text-muted-foreground">
        Your desktop assistant. It can look at tmx and message the head; tmx can hand it tasks that need your apps, browser or accounts.
      </p>
      {!st ? (
        failed ? (
          <div className="rounded-xl border bg-card px-4 py-3 text-sm text-muted-foreground">This daemon has no Companion support. Update tmx.</div>
        ) : (
          <Skeleton className="h-36 rounded-xl" />
        )
      ) : (
        <div className="space-y-5">
          <div className="divide-y rounded-xl border bg-card" data-companion-status>
            <ReadLink st={st} onChange={refresh} />
            <TaskLink st={st} onChange={refresh} />
          </div>
          <div>
            <div className="mb-2 flex h-7 items-center gap-2">
              <h3 className="text-xs font-medium text-muted-foreground">Tasks handed to Companion</h3>
              <Button size="xs" variant="outline" className="ml-auto" onClick={() => openHandOff()}>
                <Sparkles />
                Hand to Companion
              </Button>
            </div>
            {list.length === 0 ? (
              <div className="flex items-start gap-2.5 rounded-xl border border-dashed px-4 py-3 text-xs text-muted-foreground">
                <BookOpen className="mt-px size-3.5 shrink-0" />
                <span>
                  None yet. Hand one over from a question in Needs you or from the head's page, or let an agent ask: turn on “Hand tasks to Companion” in its
                  profile.
                </span>
              </div>
            ) : (
              <div className={cn("divide-y overflow-hidden rounded-xl border bg-card")}>
                {list.slice(0, 30).map((t) => (
                  <TaskRow key={t.id} t={t} />
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </Section>
  );
}
