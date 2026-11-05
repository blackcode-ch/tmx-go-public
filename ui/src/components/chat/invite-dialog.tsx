// Invite someone: a big QR code (drawn here), the link to copy, its expiry; a new code or
// revoke. A DM invite is for one person, once; a group invite works for several people until
// it expires (the group's admin only).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Loader2, RefreshCw, Users, User, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import { CopyButton } from "@/components/copy-button";
import type { Invite, InviteKind } from "@/lib/chat-types";
import { chatApi, chatUI, expiresIn, sortedChats, useChats } from "@/lib/chats";
import { navigate, paths } from "@/lib/router";
import { cn } from "@/lib/utils";
import { QRCode } from "./qr-code";

const NEW_GROUP = "__new";

function KindSwitch({ kind, onChange }: { kind: InviteKind; onChange: (k: InviteKind) => void }) {
  const opt = (k: InviteKind, Icon: typeof User, label: string) => (
    <button
      type="button"
      role="radio"
      aria-checked={kind === k}
      onClick={() => onChange(k)}
      className={cn(
        "flex h-7 flex-1 items-center justify-center gap-1.5 rounded-md text-[0.8125rem] text-muted-foreground outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring",
        kind === k && "bg-background font-medium text-foreground shadow-sm ring-1 ring-border",
      )}
    >
      <Icon className="size-3.5" />
      {label}
    </button>
  );
  return (
    <div role="radiogroup" aria-label="Who to invite" className="flex gap-1 rounded-lg bg-muted p-1">
      {opt("dm", User, "A person")}
      {opt("group", Users, "To a group")}
    </div>
  );
}

export function InviteDialog() {
  const { open, kind: initialKind, groupId: initialGroup } = useChats((s) => s.invite);
  const chats = useChats((s) => s.chats);
  const groups = useMemo(() => sortedChats(chats).filter((c) => c.kind === "group" && c.admin && c.status === "active"), [chats]);
  const [kind, setKind] = useState<InviteKind>("dm");
  const [groupId, setGroupId] = useState("");
  const [newName, setNewName] = useState("");
  const [invite, setInvite] = useState<Invite | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const seq = useRef(0);

  const create = useCallback(async (k: InviteKind, gid: string, replace?: Invite | null) => {
    const my = ++seq.current;
    setBusy(true);
    setError("");
    try {
      if (replace && !replace.revoked) await chatApi.revokeInvite(replace.id).catch(() => undefined);
      const inv = await chatApi.createInvite(k, k === "group" ? gid : undefined);
      if (my === seq.current) setInvite(inv);
    } catch (e) {
      if (my === seq.current) {
        setInvite(null);
        setError((e as Error).message || "The invite wasn't created.");
      }
    } finally {
      if (my === seq.current) setBusy(false);
    }
  }, []);

  // On open: pick the target and make a code at once (no extra click).
  useEffect(() => {
    if (!open) return;
    const k = initialKind;
    const gid = initialGroup ?? (k === "group" ? (groups[0]?.id ?? NEW_GROUP) : "");
    setKind(k);
    setGroupId(gid);
    setNewName("");
    setInvite(null);
    setError("");
    if (k === "dm" || (gid && gid !== NEW_GROUP)) void create(k, gid);
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  function switchKind(k: InviteKind) {
    if (k === kind) return;
    setKind(k);
    setInvite(null);
    setError("");
    if (k === "dm") void create("dm", "");
    else {
      const gid = groups[0]?.id ?? NEW_GROUP;
      setGroupId(gid);
      if (gid !== NEW_GROUP) void create("group", gid);
      else seq.current++;
    }
  }

  async function createGroup(e: React.FormEvent) {
    e.preventDefault();
    if (!newName.trim() || busy) return;
    setBusy(true);
    setError("");
    try {
      const g = await chatApi.createGroup(newName.trim());
      useChats.setState((s) => ({ chats: { ...s.chats, [g.id]: g } }));
      setGroupId(g.id);
      await create("group", g.id);
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  }

  async function revoke() {
    if (!invite) return;
    try {
      await chatApi.revokeInvite(invite.id);
      setInvite({ ...invite, revoked: true });
      toast("Invite revoked", { description: "The code and the link no longer work." });
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  const group = groups.find((g) => g.id === groupId);
  const live = invite && !invite.revoked;

  return (
    <Dialog open={open} onOpenChange={(v) => !v && chatUI.closeInvite()}>
      <DialogContent className="grid-cols-1 sm:max-w-[27.5rem]">
        <DialogHeader>
          <DialogTitle>Invite someone</DialogTitle>
          <DialogDescription>
            Send them the QR code or the link. In their tmx: Chats → Join. No accounts, no setup: your two tmx connect directly.
          </DialogDescription>
        </DialogHeader>

        <KindSwitch kind={kind} onChange={switchKind} />

        {kind === "group" && (
          <div className="grid gap-2">
            <Select
              value={groupId}
              onValueChange={(v) => {
                setGroupId(v);
                setInvite(null);
                if (v !== NEW_GROUP) void create("group", v);
                else seq.current++;
              }}
            >
              <SelectTrigger className="w-full" aria-label="Group">
                <SelectValue placeholder="Choose a group" />
              </SelectTrigger>
              <SelectContent>
                {groups.map((g) => (
                  <SelectItem key={g.id} value={g.id}>
                    <Users className="text-muted-foreground" />
                    {g.name}
                  </SelectItem>
                ))}
                {groups.length > 0 && <SelectSeparator />}
                <SelectItem value={NEW_GROUP}>New group…</SelectItem>
              </SelectContent>
            </Select>
            {groupId === NEW_GROUP && (
              <form onSubmit={createGroup} className="flex gap-2">
                <Input value={newName} onChange={(e) => setNewName(e.target.value)} placeholder="Group name, e.g. Weekend trip" autoFocus aria-label="New group name" />
                <Button type="submit" disabled={!newName.trim() || busy}>
                  {busy && <Loader2 className="animate-spin" />}
                  Create
                </Button>
              </form>
            )}
          </div>
        )}

        <div className="flex flex-col items-center gap-3 pt-1">
          <div className="relative flex size-[15.75rem] items-center justify-center">
            {live ? (
              <QRCode value={invite.link} size={232} label={kind === "group" ? `Invite to ${group?.name ?? "the group"}` : "Invite QR code"} />
            ) : (
              <div className="flex size-[15.75rem] flex-col items-center justify-center gap-2 rounded-xl border border-dashed text-center text-xs text-muted-foreground">
                {busy ? (
                  <Loader2 className="size-5 animate-spin" />
                ) : invite?.revoked ? (
                  <>
                    <X className="size-5" />
                    Revoked
                  </>
                ) : kind === "group" && groupId === NEW_GROUP ? (
                  "Name the group to get its code"
                ) : (
                  "No code yet"
                )}
              </div>
            )}
          </div>
          {live && (
            <p className="text-center text-xs text-muted-foreground">
              {invite.kind === "dm"
                ? `For one person · expires ${expiresIn(invite.expires_at)}`
                : `For anyone with it · expires ${expiresIn(invite.expires_at)} · up to 20 members`}
            </p>
          )}
        </div>

        {live && (
          <div className="flex min-w-0 items-center gap-1 rounded-lg border bg-muted/40 py-1 pr-1 pl-2.5">
            <code className="min-w-0 flex-1 truncate font-mono text-[0.7188rem] text-muted-foreground" title={invite.link}>
              {invite.link}
            </code>
            <CopyButton text={invite.link} label="Copy link" />
          </div>
        )}

        {error && <p className="rounded-md border border-state-error/30 bg-diff-del-bg px-3 py-2 text-xs text-foreground">{error}</p>}

        <div className="flex flex-wrap items-center gap-2">
          <Button variant="ghost" size="sm" onClick={() => void revoke()} disabled={!live} className="text-muted-foreground">
            Revoke
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void create(kind, groupId, invite)}
            disabled={busy || (kind === "group" && (!groupId || groupId === NEW_GROUP))}
          >
            <RefreshCw />
            New code
          </Button>
          <div className="ml-auto flex gap-2">
            {kind === "group" && group && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  chatUI.closeInvite();
                  navigate(paths.chat(group.id));
                }}
              >
                Open group
              </Button>
            )}
            <Button size="sm" onClick={() => chatUI.closeInvite()}>
              Done
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
