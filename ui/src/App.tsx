import { PhoneNotificationBanner, PhoneNotificationsProvider } from "@/components/phone-notifications";
import { useEffect } from "react";
import { MessagePinsStrip } from "@/components/message-pins";
import { Inbox, KeyRound, Menu, PlugZap, RefreshCw, Search } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { approvalHeadline } from "@/components/approval";
import { InviteDialog } from "@/components/chat/invite-dialog";
import { JoinDialog } from "@/components/chat/join-dialog";
import { CommandPalette } from "@/components/command-palette";
import { ReconnectingPill } from "@/components/connection";
import { NewProjectDialog } from "@/components/new-project-dialog";
import { HandOffDialog } from "@/components/companion/hand-off-dialog";
import { OwnerConfirmDialog } from "@/components/owner-confirm";
import { EmptyState } from "@/components/page";
import { ShortcutsDialog } from "@/components/shortcuts-dialog";
import { Sidebar, SidebarContent } from "@/components/sidebar";
import { useGlobalHotkeys } from "@/hooks/use-hotkeys";
import { loadSafety } from "@/lib/owner-gate";
import { loadUpdate } from "@/lib/update";
import { WindowUpdateBanner } from "@/components/update-banner";
import { href, navigate, paths, useRoute } from "@/lib/router";
import { onApprovalCreated, pendingApprovals, retryNow, useStore } from "@/lib/store";
import { summarizeTool } from "@/lib/tools";
import { ui, useUI } from "@/lib/ui";
import { AgentPage } from "@/pages/agent";
import { ChatPage } from "@/pages/chat";
import { ChatsPage } from "@/pages/chats";
import { CompanionPage } from "@/pages/companion";
import { InboxPage } from "@/pages/inbox";
import { OverviewPage } from "@/pages/overview";
import { PeopleBookPage, PromisesPage } from "@/pages/people";
import { ProjectPage } from "@/pages/project";
import { ProfilePage } from "@/pages/profile";
import { RecordingsPage } from "@/pages/recordings";
import { RecordingBar } from "@/components/recorder/recording-bar";
import { DictationBar } from "@/components/dictation-bar";
import { StartRecordingDialog } from "@/components/recorder/start-dialog";
import { CameraTile } from "@/components/recorder/camera-preview";
import { SettingsPage } from "@/pages/settings";
import { MatrixPage } from "@/pages/matrix";
import { WhatsAppPage } from "@/pages/whatsapp";
import { UsagePage } from "@/pages/usage";
import { FeaturesPage } from "@/pages/features";
import { waSendShownAt } from "@/lib/whatsapp";
import { PairDevice } from "@/components/device-pairing";
import { BrandMark } from "@/components/brand-mark";

function Routes() {
  const r = useRoute();
  switch (r.name) {
    case "overview":
      return <OverviewPage />;
    case "inbox":
      return <InboxPage />;
    case "matrix":
      return <MatrixPage />;
    case "settings":
      return <SettingsPage />;
    case "profile":
      return <ProfilePage key={r.slug} slug={r.slug} />;
    case "agent":
      return <AgentPage key={r.id} id={r.id} tab={r.tab} />;
    case "project":
      return <ProjectPage key={r.id} id={r.id} />;
    case "chats":
      return <ChatsPage />;
    case "chat":
      return <ChatPage key={r.id} id={r.id} view={r.view} />;
    case "recordings":
      return <RecordingsPage id={r.id} />;
    case "whatsapp":
      return <WhatsAppPage jid={r.jid} />;
    case "companion":
      return <CompanionPage />;
    case "features":
      return <FeaturesPage />;
    case "usage":
      return <UsagePage />;
    case "people":
      return <PeopleBookPage key="people" page="people" />;
    case "clients":
      return <PeopleBookPage key="clients" page="clients" />;
    case "promises":
      return <PromisesPage />;
    default:
      return (
        <div className="flex flex-1 items-center justify-center p-6">
          <EmptyState icon={Search} title="Nothing here" className="w-full max-w-md border-none bg-transparent">
            <a className="text-brand hover:underline" href={href(paths.overview())}>
              Back to overview
            </a>
          </EmptyState>
        </div>
      );
  }
}

function MobileBar() {
  const machine = useStore((s) => s.machine);
  const pending = pendingApprovals(useStore((s) => s.approvals)).length;
  return (
    <div className="flex h-12 shrink-0 items-center gap-1 border-b bg-sidebar px-2 md:hidden">
      <Button variant="ghost" size="icon-sm" aria-label="Open navigation" onClick={() => ui.openMobileNav(true)}>
        <Menu className="size-4" />
      </Button>
      <BrandMark className="ml-0.5" />
      <span className="truncate text-[0.8125rem] font-semibold">{machine || "tmx"}</span>
      <ReconnectingPill className="ml-1" />
      <div className="ml-auto flex items-center gap-0.5">
        <Button variant="ghost" size="icon-sm" aria-label="Search" onClick={() => ui.openPalette(true)}>
          <Search className="size-4" />
        </Button>
        <Button variant="ghost" size="sm" aria-label="Needs you" asChild className="gap-1.5 px-2">
          <a href={href(paths.inbox())}>
            <Inbox className="size-4" />
            {pending > 0 && <span className="tabular rounded-full bg-state-waiting px-1.5 text-2xs font-semibold text-black/80">{pending}</span>}
          </a>
        </Button>
      </div>
    </div>
  );
}

function MobileNav() {
  const open = useUI((s) => s.mobileNav);
  return (
    <Sheet open={open} onOpenChange={(v) => ui.openMobileNav(v)}>
      <SheetContent side="left" showCloseButton={false} className="w-[18.125rem] gap-0 bg-sidebar p-0 sm:max-w-[18.125rem]" aria-describedby={undefined}>
        <SheetTitle className="sr-only">Navigation</SheetTitle>
        <SheetDescription className="sr-only">Agents and projects on this machine</SheetDescription>
        <SidebarContent onNavigate={() => ui.openMobileNav(false)} />
      </SheetContent>
    </Sheet>
  );
}

function useApprovalToasts() {
  useEffect(
    () =>
      onApprovalCreated((a) => {
        const st = useStore.getState();
        const agent = st.agents[a.agent_id];
        const h = window.location.hash;
        if (h.startsWith("#/inbox") || (agent && h.startsWith("#" + paths.agent(agent.id))) || waSendShownAt(a, h)) return;
        const s = summarizeTool(a.tool, a.input, agent?.cwd);
        toast(`${agent?.name ?? "An agent"} ${approvalHeadline(a)}`, {
          description: a.kind === "question" || a.kind === "whatsapp" ? a.question : a.kind === "brake" ? a.description : [s.label, s.detail].filter(Boolean).join(" · "),
          action: { label: "Review", onClick: () => navigate(paths.inbox() + "?id=" + a.id) },
          duration: 8000,
        });
      }),
    [],
  );
}

function Boot() {
  return (
    <div className="flex h-full">
      <div className="hidden w-[16.25rem] shrink-0 flex-col gap-3 border-r bg-sidebar p-3 pt-[3.75rem] md:flex">
        <Skeleton className="h-16 rounded-lg" />
        <Skeleton className="h-6 w-3/4" />
        <Skeleton className="h-6 w-2/3" />
        <Skeleton className="mt-4 h-5 w-1/2" />
        <Skeleton className="h-5 w-2/3" />
        <Skeleton className="h-5 w-3/5" />
      </div>
      <div className="flex-1 p-8 pt-[4.5rem]">
        <div className="mx-auto max-w-[65rem] space-y-6">
          <Skeleton className="h-40 rounded-xl" />
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            <Skeleton className="h-32 rounded-xl" />
            <Skeleton className="h-32 rounded-xl" />
            <Skeleton className="h-32 rounded-xl" />
          </div>
        </div>
      </div>
    </div>
  );
}

function Blocked({ kind }: { kind: "unauthorized" | "offline" }) {
  const msg = useStore((s) => s.statusMessage);
  const base = useStore((s) => s.transport?.baseURL);
  return (
    <div className="drag-region flex h-full items-center justify-center p-6">
      {kind === "unauthorized" && window.location.protocol === "https:" ? <PairDevice /> : kind === "unauthorized" ? (
        <EmptyState icon={KeyRound} title="This page needs your owner token" className="w-full max-w-md">
          Open tmx with <code className="rounded border bg-muted px-1 font-mono text-xs">tmx open</code> in a terminal: it prints a link
          that signs this page in. The token stays in this tab only.
          {msg && <p className="mt-3 text-xs text-subtle-foreground">{msg}</p>}
        </EmptyState>
      ) : (
        <EmptyState
          icon={PlugZap}
          title="Can't reach the tmx daemon"
          className="w-full max-w-md"
          actions={
            <Button variant="outline" size="sm" onClick={retryNow}>
              <RefreshCw />
              Try again
            </Button>
          }
        >
          Retrying{base ? ` ${base}` : ""} in the background. Start it with{" "}
          <code className="rounded border bg-muted px-1 font-mono text-xs">tmx daemon</code>; your agents keep running either way.
        </EmptyState>
      )}
    </div>
  );
}

export default function App() {
  const status = useStore((s) => s.status);
  const transport = useStore((s) => s.transport);
  useGlobalHotkeys();
  useApprovalToasts();
  useEffect(() => {
    if (status === "ready") {
      void loadSafety(); // the gate's state decides how Allow looks
      void loadUpdate(); // the sidebar shows when a release is on its way
    }
  }, [status]);
  const desktopMac = !!transport?.desktop && transport.platform === "darwin";

  return (
    <PhoneNotificationsProvider><TooltipProvider>
      {status === "booting" ? (
        <Boot />
      ) : status === "unauthorized" || status === "offline" ? (
        <Blocked kind={status} />
      ) : (
        <div className="flex h-full min-h-0 overflow-hidden">
          <Sidebar desktopMac={desktopMac} />
          <main className="flex min-w-0 flex-1 flex-col">
            <RecordingBar />
            <DictationBar />
            <WindowUpdateBanner />
            <PhoneNotificationBanner />
            <MobileBar />
            <MessagePinsStrip />
            <Routes />
          </main>
          <MobileNav />
          <CommandPalette />
          <ShortcutsDialog />
          <NewProjectDialog />
          <HandOffDialog />
          <InviteDialog />
          <JoinDialog />
          <OwnerConfirmDialog />
          <StartRecordingDialog />
          <CameraTile />
        </div>
      )}
      <Toaster position="bottom-right" closeButton={false} />
    </TooltipProvider></PhoneNotificationsProvider>
  );
}
