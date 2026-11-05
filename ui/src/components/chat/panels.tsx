// The chat page's side panel: contact details (with the safety code) for a DM, members for a
// group (the admin can hand the group to another member); and the selector of the local
// agent that takes part in a chat.
import { useEffect, useMemo, useState } from "react";
import { Bot, Crown, Fingerprint, Loader2, LogOut, MoreHorizontal, QrCode, ShieldCheck, TriangleAlert, Trash2, UserMinus, Users } from "lucide-react";
import { toast } from "sonner";
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
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { AgentAvatar } from "@/components/agent-avatar";
import { CopyButton } from "@/components/copy-button";
import { absoluteTime, relativeTime } from "@/lib/format";
import { useNow } from "@/hooks/use-now";
import { useGate } from "@/lib/owner-gate";
import type { Chat, ChatMember, Contact } from "@/lib/chat-types";
import { currentMembers } from "@/lib/chat-types";
import { chatApi, chatUI, useChats } from "@/lib/chats";
import { navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import type { Agent } from "@/lib/types";
import { cn } from "@/lib/utils";
import { PersonAvatar, ReachDot, reachLabel } from "./chat-bits";

const NONE = "__none";

/** Which local agent reads and may answer in this chat ("people only" = none). */
export function AgentPicker({ chat, className }: { chat: Chat; className?: string }) {
  const agents = useStore((s) => s.agents);
  const headId = useStore((s) => s.headId);
  const options = useMemo(() => {
    const all = Object.values(agents);
    const head = all.filter((a) => a.id === headId);
    const rest = all.filter((a) => a.id !== headId).sort((a, b) => a.name.localeCompare(b.name));
    return [...head, ...rest];
  }, [agents, headId]);
  const value = chat.agent_id && agents[chat.agent_id] ? chat.agent_id : chat.agent_id ? chat.agent_id : NONE;
  const over = chat.status === "left" || chat.status === "removed";

  async function pick(v: string) {
    try {
      const c = await chatApi.patchChat(chat.id, { agent_id: v === NONE ? "" : v });
      useChats.setState((s) => ({ chats: { ...s.chats, [c.id]: c } }));
      toast(v === NONE ? "Only people in this chat now" : `${agents[v]?.name ?? "The agent"} takes part in this chat`, {
        description: v === NONE ? "No agent here gets its messages." : "It gets new messages as mail, labelled as coming from other people.",
      });
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <Select value={value} onValueChange={(v) => void pick(v)} disabled={over}>
      <Tooltip>
        <TooltipTrigger asChild>
          <SelectTrigger size="sm" className={cn("h-8 max-w-[12.5rem] gap-1.5 text-[0.8125rem]", className)} aria-label="Agent taking part">
            <Bot className="size-3.5 text-muted-foreground" />
            <SelectValue />
          </SelectTrigger>
        </TooltipTrigger>
        <TooltipContent>The agent of yours that reads this chat and may answer in it</TooltipContent>
      </Tooltip>
      <SelectContent position="popper" align="end">
        {options.map((a: Agent) => (
          <SelectItem key={a.id} value={a.id}>
            <AgentAvatar agent={a} className="size-4 rounded text-[0.5625rem]" />
            {a.name}
            {a.id === headId && a.name !== "head" && <span className="text-2xs text-muted-foreground">head</span>}
          </SelectItem>
        ))}
        {chat.agent_id && !agents[chat.agent_id] && <SelectItem value={chat.agent_id}>{chat.agent_name || "Removed agent"}</SelectItem>}
        <SelectSeparator />
        <SelectItem value={NONE}>People only</SelectItem>
      </SelectContent>
    </Select>
  );
}

function PanelSection({ title, children, right }: { title: string; children: React.ReactNode; right?: React.ReactNode }) {
  return (
    <section className="border-b px-4 py-4 last:border-b-0">
      <div className="mb-2 flex items-center">
        <h3 className="text-2xs font-medium tracking-wide text-subtle-foreground uppercase">{title}</h3>
        {right && <div className="ml-auto">{right}</div>}
      </div>
      {children}
    </section>
  );
}

/** The safety code in groups, large enough to read out loud. */
export function SafetyCode({ code }: { code: string }) {
  if (!code) return <p className="text-xs text-muted-foreground">No safety code yet.</p>;
  return (
    <div className="flex flex-wrap gap-x-3 gap-y-1 font-mono text-[1.0625rem] font-medium tracking-[0.06em] tabular-nums" aria-label={`Safety code ${code}`}>
      {code.split(/\s+/).map((g, i) => (
        <span key={i}>{g}</span>
      ))}
    </div>
  );
}

export function ContactPanel({ contact, chat }: { contact: Contact | undefined; chat: Chat }) {
  const [name, setName] = useState(contact?.name ?? chat.name);
  const [note, setNote] = useState(contact?.note ?? "");
  const [confirm, setConfirm] = useState(false);
  useEffect(() => setName(contact?.name ?? chat.name), [contact?.name, chat.name]);
  useEffect(() => setNote(contact?.note ?? ""), [contact?.note]);

  if (!contact) {
    return (
      <div className="p-4 text-sm text-muted-foreground">
        {chat.status === "removed" ? "You removed this contact. Their tmx can no longer reach yours." : "This contact is not in your list any more."}
      </div>
    );
  }

  async function save(patch: { name?: string; note?: string }) {
    try {
      const c = await chatApi.patchContact(contact!.peer_id, patch);
      useChats.setState((s) => ({ contacts: { ...s.contacts, [c.peer_id]: c } }));
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <div>
      <div className="flex flex-col items-center px-4 pt-6 pb-4 text-center">
        <PersonAvatar name={contact.name} size="lg" />
        <p className="mt-2.5 text-[0.9375rem] font-semibold">{contact.name}</p>
        {contact.remote_name && contact.remote_name !== contact.name && (
          <p className="text-xs text-muted-foreground">Calls themselves {contact.remote_name}</p>
        )}
        <p className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
          <ReachDot r={contact.reachability} />
          {reachLabel(contact.reachability)}
        </p>
      </div>
      <PanelSection title="Safety code">
        <SafetyCode code={contact.safety_code} />
        <p className="mt-2 flex gap-1.5 text-xs leading-relaxed text-muted-foreground">
          <ShieldCheck className="mt-0.5 size-3.5 shrink-0" />
          <span>Compare it with {contact.name} in person or on a call. The same code on both screens means you're connected to the right tmx.</span>
        </p>
      </PanelSection>
      <PanelSection title="Name and note">
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim() !== contact.name) void save({ name: name.trim() });
          }}
        >
          <Input value={name} onChange={(e) => setName(e.target.value)} aria-label="Name shown here" className="h-8" />
          <Button type="submit" size="sm" variant="outline" disabled={!name.trim() || name.trim() === contact.name}>
            Save
          </Button>
        </form>
        <Textarea
          value={note}
          onChange={(e) => setNote(e.target.value)}
          onBlur={() => note !== contact.note && void save({ note })}
          placeholder="A note for yourself"
          aria-label="Note"
          className="mt-2 min-h-16 resize-none text-[0.8125rem]"
        />
      </PanelSection>
      <PanelSection title="Details">
        <dl className="grid gap-1.5 text-xs">
          <div className="flex gap-2">
            <dt className="w-16 shrink-0 text-muted-foreground">Added</dt>
            <dd title={absoluteTime(contact.added_at)}>{relativeTime(contact.added_at)}</dd>
          </div>
          <div className="flex items-start gap-2">
            <dt className="w-16 shrink-0 pt-0.5 text-muted-foreground">Peer ID</dt>
            <dd className="flex min-w-0 items-start gap-1">
              <code className="min-w-0 font-mono text-[0.6875rem] break-all text-muted-foreground">{contact.peer_id}</code>
              <CopyButton text={contact.peer_id} label="Copy peer ID" className="-mt-0.5" />
            </dd>
          </div>
        </dl>
      </PanelSection>
      <div className="px-4 py-4">
        <Button variant="ghost" size="sm" className="w-full justify-start text-destructive hover:text-destructive" onClick={() => setConfirm(true)}>
          <UserMinus /> Remove contact…
        </Button>
      </div>
      <AlertDialog open={confirm} onOpenChange={setConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove {contact.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Their tmx can no longer reach yours, and messages still queued for them are dropped. The chat history stays here (read-only) until you
              delete it. To talk again, one of you sends a new invite.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={async () => {
                try {
                  await chatApi.removeContact(contact.peer_id);
                  toast(`Removed ${contact.name}`);
                } catch (e) {
                  toast.error((e as Error).message);
                }
              }}
            >
              Remove contact
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/** Radix: a dialog opened from a menu item opens after the menu has closed. */
const after = (fn: () => void) => () => setTimeout(fn, 0);

function MemberRow({
  m,
  chat,
  onRemove,
  onMakeAdmin,
}: {
  m: ChatMember;
  chat: Chat;
  onRemove: (m: ChatMember) => void;
  onMakeAdmin: (m: ChatMember) => void;
}) {
  const contact = useChats((s) => s.contacts[m.peer_id]);
  const offered = chat.admin_offer?.peer_id === m.peer_id;
  return (
    <li className="group/m flex h-9 items-center gap-2.5">
      <PersonAvatar name={m.name || "?"} size="sm" className="size-6 text-[0.625rem]" />
      <span className="min-w-0 truncate text-[0.8125rem]">
        {m.me && (!m.name || m.name === "You") ? "You" : m.name || "Someone"}
        {m.me && m.name && m.name !== "You" && <span className="text-muted-foreground"> (you)</span>}
      </span>
      {m.role === "admin" && (
        <span className="inline-flex h-[1.125rem] items-center gap-1 rounded-[5px] border px-1.5 text-[0.6562rem] text-muted-foreground" title="Admin: invites, removes and renames">
          <Crown className="size-3" /> admin
        </span>
      )}
      {offered && (
        <span className="inline-flex h-[1.125rem] items-center gap-1 rounded-[5px] border border-dashed px-1.5 text-[0.6562rem] text-muted-foreground" title="You offered them the admin role">
          <Crown className="size-3" /> offered
        </span>
      )}
      <span className="ml-auto flex items-center gap-1">
        {contact && <ReachDot r={contact.reachability} />}
        {chat.admin && !m.me && chat.status === "active" && (
          <DropdownMenu modal={false}>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={`${m.name || "Member"} options`}
                className="text-muted-foreground opacity-0 group-hover/m:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100 pointer-coarse:opacity-100"
              >
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-48" onCloseAutoFocus={(e) => e.preventDefault()}>
              <DropdownMenuItem disabled={!!chat.admin_offer} onSelect={after(() => onMakeAdmin(m))}>
                <Crown /> Make admin…
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" onSelect={after(() => onRemove(m))}>
                <UserMinus /> Remove…
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </span>
    </li>
  );
}

/** "Waiting for Bo's tmx…" while our offer of the admin role has no answer yet. */
function AdminOfferNote({ chat }: { chat: Chat }) {
  const now = useNow(5_000);
  const [busy, setBusy] = useState(false);
  const o = chat.admin_offer;
  if (!o) return null;
  const name = o.name || "the member";
  const slow = o.sent && now - o.at > 15_000;
  return (
    <div className="mt-3 rounded-lg border bg-muted/40 px-3 py-2.5" data-admin-offer>
      <p className="flex items-center gap-1.5 text-[0.8125rem] font-medium">
        <Loader2 className="size-3.5 animate-spin text-muted-foreground" /> Waiting for {name}'s tmx…
      </p>
      <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
        {!o.sent
          ? `${name}'s tmx is offline. ${name} becomes the admin as soon as it's back.`
          : slow
            ? `${name}'s tmx has the offer but hasn't taken over. It may need an update first.`
            : `${name} becomes the admin in a moment.`}
        {o.then_leave && " You leave the group then."}
      </p>
      <Button
        variant="outline"
        size="xs"
        className="mt-2"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          try {
            const c = await chatApi.cancelAdmin(chat.id);
            useChats.setState((s) => ({ chats: { ...s.chats, [c.id]: c } }));
            toast("Offer withdrawn", { description: "You're still the admin." });
          } catch (e) {
            toast.error((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        Cancel
      </Button>
    </div>
  );
}

/**
 * Make another member the admin: who, what changes, members whose tmx may need an update,
 * "Leave the group afterwards"; then the daemon asks for Touch ID / Windows Hello.
 */
function MakeAdminDialog({
  chat,
  open,
  onOpenChange,
  member,
  leave: leaveFirst,
}: {
  chat: Chat;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  /** the member chosen from its menu; none = pick one here */
  member?: ChatMember;
  leave: boolean;
}) {
  const others = currentMembers(chat).filter((m) => !m.me);
  const [peer, setPeer] = useState("");
  const [leave, setLeave] = useState(leaveFirst);
  const method = useGate((s) => s.safety?.gate.method_label);
  useEffect(() => {
    if (!open) return;
    setPeer(member?.peer_id ?? others[0]?.peer_id ?? "");
    setLeave(leaveFirst);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- reset only when it opens
  }, [open]);
  const target = others.find((m) => m.peer_id === peer);
  const name = target?.name || "They";
  const old = others.filter((m) => m.peer_id !== peer && !m.handover).length;

  async function submit() {
    if (!target) return;
    try {
      const c = await chatApi.offerAdmin(chat.id, target.peer_id, leave);
      useChats.setState((s) => ({ chats: { ...s.chats, [c.id]: c } }));
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent onCloseAutoFocus={(e) => e.preventDefault()}>
        <AlertDialogHeader>
          <AlertDialogTitle>{member ? `Make ${name} the admin?` : "Make someone else the admin"}</AlertDialogTitle>
          <AlertDialogDescription>
            {name} can add and remove people and rename the group; you become a member. Only {target ? name : "the new admin"} can make you the admin again.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {!member && (
          <Select value={peer} onValueChange={setPeer}>
            <SelectTrigger className="w-full" aria-label="New admin">
              <SelectValue placeholder="Choose a member" />
            </SelectTrigger>
            <SelectContent position="popper">
              {others.map((m) => (
                <SelectItem key={m.peer_id} value={m.peer_id}>
                  <PersonAvatar name={m.name || "?"} size="sm" className="size-4 text-[0.5rem]" />
                  {m.name || "Someone"}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {(old > 0 || (target && !target.handover)) && (
          <div className="flex gap-2 rounded-lg border border-state-waiting/30 bg-state-waiting/10 px-3 py-2 text-xs leading-relaxed">
            <TriangleAlert className="mt-0.5 size-3.5 shrink-0 text-state-waiting" />
            <div className="space-y-1">
              {old > 0 && (
                <p>
                  {old} {old === 1 ? "member" : "members"} may need to update tmx first; until then their member list stays as it is.
                </p>
              )}
              {target && !target.handover && <p>If {name}'s tmx needs an update, the handover waits until it has one.</p>}
            </div>
          </div>
        )}
        <label className="flex cursor-pointer items-center gap-2.5 text-[0.8125rem]">
          <Checkbox checked={leave} onCheckedChange={(v) => setLeave(v === true)} />
          Leave the group afterwards
        </label>
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Fingerprint className="size-3.5 shrink-0" />
          Next you confirm it's you with {method || "Touch ID or your password"} on this machine.
        </p>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction disabled={!target} onClick={() => void submit()}>
            <Crown /> Make admin
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export function MembersPanel({ chat }: { chat: Chat }) {
  const members = currentMembers(chat);
  const former = (chat.members ?? []).filter((m) => m.left_at);
  const [removing, setRemoving] = useState<ChatMember | null>(null);
  const [leaving, setLeaving] = useState(false);
  // kept while the dialog closes, so its title does not change under the animation
  const [handing, setHanding] = useState<{ open: boolean; member?: ChatMember; leave: boolean }>({ open: false, leave: false });
  const over = chat.status === "left" || chat.status === "removed";
  const admin = members.find((m) => m.role === "admin");
  const others = members.filter((m) => !m.me);

  return (
    <div>
      <div className="flex flex-col items-center px-4 pt-6 pb-4 text-center">
        <span className="flex size-10 items-center justify-center rounded-lg border bg-muted/60 text-muted-foreground">
          <Users className="size-5" />
        </span>
        <p className="mt-2.5 text-[0.9375rem] font-semibold">{chat.name}</p>
        <p className="text-xs text-muted-foreground">
          {members.length} {members.length === 1 ? "member" : "members"}
          {admin && !admin.me ? ` · run by ${admin.name}` : chat.admin ? " · you run it" : ""}
        </p>
      </div>
      <PanelSection
        title="Members"
        right={
          chat.admin && !over ? (
            <Button variant="ghost" size="xs" className="text-muted-foreground" onClick={() => chatUI.openInvite("group", chat.id)}>
              <QrCode /> Invite
            </Button>
          ) : undefined
        }
      >
        <ul>
          {members.map((m) => (
            <MemberRow key={m.peer_id} m={m} chat={chat} onRemove={setRemoving} onMakeAdmin={(x) => setHanding({ open: true, member: x, leave: false })} />
          ))}
        </ul>
        {members.length >= 20 && <p className="mt-2 text-xs text-muted-foreground">The group is full (20 members).</p>}
        <AdminOfferNote chat={chat} />
      </PanelSection>
      {former.length > 0 && (
        <PanelSection title="Left">
          <ul className="text-[0.8125rem] text-muted-foreground">
            {former.map((m) => (
              <li key={m.peer_id} className="flex h-7 items-center gap-2">
                <PersonAvatar name={m.name || "?"} size="sm" className="opacity-60" />
                <span className="truncate">{m.name}</span>
              </li>
            ))}
          </ul>
        </PanelSection>
      )}
      <PanelSection title="How it works">
        <p className="text-xs leading-relaxed text-muted-foreground">
          Every message goes straight from its writer's tmx to each member's, signed by the writer, so the group keeps talking while the admin is offline. Whoever is offline gets what they missed when they're back. Only the admin invites, removes and renames, and can make another member the admin.
        </p>
      </PanelSection>
      <div className="px-4 py-4">
        {!over ? (
          <Button variant="ghost" size="sm" className="w-full justify-start text-destructive hover:text-destructive" onClick={() => setLeaving(true)}>
            <LogOut /> Leave the group…
          </Button>
        ) : (
          <Button
            variant="ghost"
            size="sm"
            className="w-full justify-start text-destructive hover:text-destructive"
            onClick={async () => {
              try {
                await chatApi.deleteChat(chat.id);
                navigate(paths.chats());
              } catch (e) {
                toast.error((e as Error).message);
              }
            }}
          >
            <Trash2 /> Delete the history
          </Button>
        )}
      </div>

      <AlertDialog open={!!removing} onOpenChange={(v) => !v && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove {removing?.name} from {chat.name}?</AlertDialogTitle>
            <AlertDialogDescription>They stop getting the group's messages and can't write to it. You can invite them again later.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={async () => {
                if (!removing) return;
                try {
                  const c = await chatApi.removeMember(chat.id, removing.peer_id);
                  useChats.setState((s) => ({ chats: { ...s.chats, [c.id]: c } }));
                } catch (e) {
                  toast.error((e as Error).message);
                }
              }}
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog open={leaving} onOpenChange={setLeaving}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Leave {chat.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              {!chat.admin
                ? "You stop getting its messages. The history stays here until you delete it."
                : others.length === 0
                  ? "You're the only member, so leaving closes the group. The history stays here until you delete it."
                  : chat.admin_offer
                    ? `You run this group, so leaving now closes it for everyone and withdraws your offer to ${chat.admin_offer.name}. The history stays on each side.`
                    : "You run this group, so leaving closes it for everyone. To keep it going without you, make someone else the admin first. The history stays on each side."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            {chat.admin && others.length > 0 && !chat.admin_offer && (
              <Button variant="outline" onClick={() => {
                setLeaving(false);
                setTimeout(() => setHanding({ open: true, leave: true }), 0);
              }}>
                <Crown /> Make someone admin first
              </Button>
            )}
            <AlertDialogAction
              variant="destructive"
              onClick={async () => {
                try {
                  const c = await chatApi.leave(chat.id);
                  useChats.setState((s) => ({ chats: { ...s.chats, [c.id]: c } }));
                } catch (e) {
                  toast.error((e as Error).message);
                }
              }}
            >
              {chat.admin ? "Close group" : "Leave"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <MakeAdminDialog
        chat={chat}
        open={handing.open}
        onOpenChange={(v) => setHanding((h) => ({ ...h, open: v }))}
        member={handing.member}
        leave={handing.leave}
      />
    </div>
  );
}
