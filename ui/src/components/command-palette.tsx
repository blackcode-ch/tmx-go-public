import { useMemo } from "react";
import { CircleDot, FolderClosed, Gauge, Inbox, Keyboard, LayoutGrid, ListChecks, MessageCircle, MessageSquare, Moon, Plus, Settings, Sparkles, Square, Sun, Waypoints } from "lucide-react";
import { useCompanionSetUp } from "@/lib/companion";
import { isLinked, sortedWaChats, useWa } from "@/lib/whatsapp";
import { WaAvatar } from "./whatsapp/wa-bits";
import { capture, openRecordDialog } from "@/lib/recorder";
import { recApi } from "@/lib/recordings";
import { useLiveRecording } from "./recorder/recording-bar";
import { CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, CommandShortcut } from "@/components/ui/command";
import { navigate, paths } from "@/lib/router";
import { sortedChats, useChats } from "@/lib/chats";
import { pendingApprovals, useStore } from "@/lib/store";
import { toggleTheme, useTheme } from "@/lib/theme";
import { ui, useUI } from "@/lib/ui";
import { agentModelLabel, STATE_LABEL } from "@/lib/format";
import { agentLabel } from "@/lib/profiles";
import { StateDot } from "./state";

export function CommandPalette() {
  const open = useUI((s) => s.palette);
  const agents = useStore((s) => s.agents);
  const profiles = useStore((s) => s.profiles);
  const projects = useStore((s) => s.projects);
  const headId = useStore((s) => s.headId);
  const pending = pendingApprovals(useStore((s) => s.approvals)).length;
  const { resolved } = useTheme();
  const chats = useChats((s) => s.chats);
  const boards = useMemo(() => sortedChats(chats).filter((c) => c.status !== "pending"), [chats]);
  const live = useLiveRecording();
  const wa = useWa((s) => (s.available && !s.offMessage ? s.status : null));
  const waChats = useWa((s) => s.chats);
  const waList = useMemo(() => (isLinked(wa) ? sortedWaChats(waChats).slice(0, 40) : []), [wa, waChats]);
  const companion = useCompanionSetUp();

  const agentList = useMemo(() => {
    const rank = { head: 0, lead: 1, worker: 2 } as const;
    return Object.values(agents).sort((a, b) => rank[a.level] - rank[b.level] || a.name.localeCompare(b.name));
  }, [agents]);

  const go = (p: string) => {
    ui.openPalette(false);
    navigate(p);
  };

  return (
    <CommandDialog open={open} onOpenChange={(v) => ui.openPalette(v)} title="Command palette" description="Jump to an agent or project, or run a command" className="top-[16%] translate-y-0 data-[state=open]:slide-in-from-top-2 sm:max-w-[36.25rem]" showCloseButton={false}>
      <CommandInput placeholder="Search agents, projects and commands…" />
      <CommandList className="scroll-thin">
        <CommandEmpty>Nothing matches.</CommandEmpty>
        <CommandGroup heading="Go to">
          <CommandItem value="overview home" onSelect={() => go(paths.overview())}>
            <LayoutGrid /> Home <CommandShortcut>G O</CommandShortcut>
          </CommandItem>
          <CommandItem value="needs you inbox approvals" onSelect={() => go(paths.inbox())}>
            <Inbox /> Needs you
            {pending > 0 && <span className="tabular text-xs text-muted-foreground">{pending} pending</span>}
            <CommandShortcut>G I</CommandShortcut>
          </CommandItem>
          <CommandItem value="matrix board topics threads head canvas" onSelect={() => go(paths.matrix())}>
            <Waypoints /> Matrix <CommandShortcut>G M</CommandShortcut>
          </CommandItem>
          {headId && (
            <CommandItem value="talk to the head" onSelect={() => go(paths.agent(headId))}>
              <MessageSquare /> Talk to the head <CommandShortcut>G H</CommandShortcut>
            </CommandItem>
          )}
          <CommandItem value="recordings screen microphone audio video" onSelect={() => go(paths.recordings())}>
            <CircleDot /> Recordings
          </CommandItem>
          {companion && (
            <CommandItem value="companion talk desktop assistant" onSelect={() => go(paths.companion())}>
              <Sparkles /> Companion
            </CommandItem>
          )}
          <CommandItem value="usage ai accounts quota limits connect claude codex antigravity" onSelect={() => go(paths.usage())}>
            <Gauge /> Usage
          </CommandItem>
          <CommandItem value="features ordered status works partial missing evidence" onSelect={() => go(paths.features())}>
            <ListChecks /> Features
          </CommandItem>
          {wa && (
            <CommandItem value="whatsapp chats messages link" onSelect={() => go(paths.whatsapp())}>
              <MessageCircle /> {isLinked(wa) ? "WhatsApp" : "Link WhatsApp…"}
            </CommandItem>
          )}
          <CommandItem value="settings policy profiles" onSelect={() => go(paths.settings())}>
            <Settings /> Settings <CommandShortcut>G S</CommandShortcut>
          </CommandItem>
        </CommandGroup>
        <CommandGroup heading="Actions">
          <CommandItem value="new project create" onSelect={() => ui.openNewProject(true)}>
            <Plus /> New project <CommandShortcut>C</CommandShortcut>
          </CommandItem>
          {live.rec ? (
            <CommandItem
              value="stop the recording"
              onSelect={() => {
                ui.openPalette(false);
                void (live.mine ? capture.stop() : recApi.setState(live.rec!.id, "stopped"));
              }}
            >
              <Square /> Stop the recording
            </CommandItem>
          ) : (
            <CommandItem
              value="start a recording record screen microphone"
              onSelect={() => {
                ui.openPalette(false);
                openRecordDialog(true);
              }}
            >
              <CircleDot /> Start a recording…
            </CommandItem>
          )}
          <CommandItem
            value="toggle theme dark light"
            onSelect={() => {
              toggleTheme();
              ui.openPalette(false);
            }}
          >
            {resolved === "dark" ? <Sun /> : <Moon />} {resolved === "dark" ? "Switch to light theme" : "Switch to dark theme"}
          </CommandItem>
          <CommandItem
            value="keyboard shortcuts help"
            onSelect={() => {
              ui.openPalette(false);
              ui.openShortcuts(true);
            }}
          >
            <Keyboard /> Keyboard shortcuts <CommandShortcut>?</CommandShortcut>
          </CommandItem>
        </CommandGroup>
        {agentList.length > 0 && (
          <CommandGroup heading="Agents">
            {agentList.map((a) => {
              const p = a.project_id ? projects[a.project_id] : undefined;
              return (
                <CommandItem key={a.id} value={`agent ${a.name} ${agentLabel(a, profiles)} ${p?.name ?? ""} ${a.level}`} onSelect={() => go(paths.agent(a.id))}>
                  <span className="flex size-4 items-center justify-center">
                    <StateDot state={a.state} />
                  </span>
                  <span className="truncate">{agentLabel(a, profiles)}</span>
                  <span className="truncate text-xs text-muted-foreground">
                    {[a.level, p?.name, agentModelLabel(a)].filter(Boolean).join(" · ")}
                  </span>
                  <span className="ml-auto text-xs text-subtle-foreground">{STATE_LABEL[a.state]}</span>
                </CommandItem>
              );
            })}
          </CommandGroup>
        )}
        {boards.length > 0 && (
          <CommandGroup heading="Chat boards">
            {boards.map((c) => (
              <CommandItem key={c.id} value={`board chat topics ${c.name} ${c.id}`} onSelect={() => go(paths.chatBoard(c.id))}>
                <Waypoints /> <span className="truncate">{c.name}</span>
                <span className="text-xs text-muted-foreground">{c.kind === "group" ? "group board" : "board"}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}
        {waList.length > 0 && (
          <CommandGroup heading="WhatsApp">
            {waList.map((c) => (
              <CommandItem key={c.jid} value={`whatsapp ${c.name} ${c.jid}`} onSelect={() => go(paths.waChat(c.jid))}>
                <WaAvatar chat={c} size="sm" /> <span className="truncate">{c.name}</span>
                <span className="text-xs text-muted-foreground">{c.kind === "group" ? "WhatsApp group" : "WhatsApp"}</span>
                {c.unread > 0 && !c.muted && <span className="tabular ml-auto text-xs text-muted-foreground">{c.unread} unread</span>}
              </CommandItem>
            ))}
          </CommandGroup>
        )}
        {Object.keys(projects).length > 0 && (
          <CommandGroup heading="Projects">
            {Object.values(projects)
              .sort((a, b) => a.name.localeCompare(b.name))
              .map((p) => (
                <CommandItem key={p.id} value={`project ${p.name} ${p.folder}`} onSelect={() => go(paths.project(p.id))}>
                  <FolderClosed /> <span className="truncate">{p.name}</span>
                  {p.status !== "active" && <span className="text-xs text-muted-foreground">{p.status}</span>}
                </CommandItem>
              ))}
          </CommandGroup>
        )}
      </CommandList>
    </CommandDialog>
  );
}
