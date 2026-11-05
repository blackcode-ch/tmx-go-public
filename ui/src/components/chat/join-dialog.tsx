// Join with a link someone sent: preview first ("Join Alex's tmx" / "Join group Trip by
// Alex"), then pair and land in the chat.
import { useEffect, useRef, useState } from "react";
import { Loader2, Users } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { InvitePreview } from "@/lib/chat-types";
import { chatApi, chatUI, expiresIn, useChats } from "@/lib/chats";
import { navigate, paths } from "@/lib/router";
import { PersonAvatar } from "./chat-bits";

const looksLikeLink = (s: string) => /^tmx:\/\/join\/\S+$/i.test(s.trim());

export function JoinDialog() {
  const { open, link: initial } = useChats((s) => s.join);
  const [link, setLink] = useState("");
  const [preview, setPreview] = useState<InvitePreview | null>(null);
  const [checking, setChecking] = useState(false);
  const [joining, setJoining] = useState(false);
  const [error, setError] = useState("");
  const seq = useRef(0);

  useEffect(() => {
    if (!open) return;
    setLink(initial ?? "");
    setPreview(null);
    setError("");
    setJoining(false);
    if (initial && looksLikeLink(initial)) void check(initial);
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  async function check(l: string) {
    const my = ++seq.current;
    setChecking(true);
    setError("");
    setPreview(null);
    try {
      const pv = await chatApi.preview(l.trim());
      if (my === seq.current) setPreview(pv);
    } catch (e) {
      if (my === seq.current) setError((e as Error).message || "This link doesn't work.");
    } finally {
      if (my === seq.current) setChecking(false);
    }
  }

  async function join(e: React.FormEvent) {
    e.preventDefault();
    if (!preview) {
      if (link.trim()) void check(link);
      return;
    }
    setJoining(true);
    setError("");
    try {
      const res = await chatApi.join(link.trim());
      useChats.setState((s) => ({
        chats: { ...s.chats, [res.chat.id]: res.chat },
        contacts: { ...s.contacts, [res.contact.peer_id]: res.contact },
      }));
      chatUI.closeJoin();
      toast.success(preview.kind === "group" ? `Joined ${preview.group_name || "the group"}` : `Connected with ${preview.name}`);
      navigate(paths.chat(res.chat.id));
    } catch (err) {
      setError((err as Error).message || "Joining didn't work.");
      setJoining(false);
    }
  }

  const group = preview?.kind === "group";

  return (
    <Dialog open={open} onOpenChange={(v) => !v && !joining && chatUI.closeJoin()}>
      <DialogContent className="grid-cols-1 sm:max-w-[28.75rem]">
        <form onSubmit={join} className="grid min-w-0 grid-cols-1 gap-4">
          <DialogHeader>
            <DialogTitle>Join with an invite</DialogTitle>
            <DialogDescription>Paste the link someone sent you (it starts with tmx://join/).</DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="join-link">Invite link</Label>
            <Input
              id="join-link"
              value={link}
              autoFocus
              spellCheck={false}
              autoComplete="off"
              placeholder="tmx://join/…"
              className="font-mono text-[0.7812rem]"
              onChange={(e) => {
                const v = e.target.value;
                setLink(v);
                setPreview(null);
                setError("");
                if (looksLikeLink(v)) void check(v);
              }}
            />
          </div>

          {checking && (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" />
              Checking the invite…
            </div>
          )}
          {preview && (
            <div className="flex items-center gap-3 rounded-xl border bg-card p-3.5">
              {group ? (
                <span className="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-muted/60 text-muted-foreground">
                  <Users className="size-5" />
                </span>
              ) : (
                <PersonAvatar name={preview.name} size="lg" />
              )}
              <div className="min-w-0">
                <p className="truncate text-[0.875rem] font-medium">
                  {group ? `Join group “${preview.group_name || "Unnamed"}” by ${preview.name}` : `Join ${preview.name}'s tmx`}
                </p>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  {group
                    ? "You'll see the members and can write to everyone."
                    : "You'll become contacts and can message each other; your agents can take part."}{" "}
                  Expires {expiresIn(preview.expires_at)}.
                </p>
              </div>
            </div>
          )}
          {error && <p className="rounded-md border border-state-error/30 bg-diff-del-bg px-3 py-2 text-xs text-foreground">{error}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => chatUI.closeJoin()} disabled={joining}>
              Cancel
            </Button>
            <Button type="submit" disabled={!link.trim() || checking || joining}>
              {joining && <Loader2 className="animate-spin" />}
              {preview ? "Join" : "Check link"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
