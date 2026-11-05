// What the owner does with a finished recording: share it into a chat (each segment is one
// file; a message carries at most 10 files of 25 MB), link it on a Board topic (a line in the
// topic's log; no media goes into the board), delete it.
import { useEffect, useMemo, useState } from "react";
import { Loader2, Users } from "lucide-react";
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
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { boardApi, loadBoard, useBoard } from "@/lib/board";
import { loadChats, sortedChats, useChats } from "@/lib/chats";
import { navigate, paths } from "@/lib/router";
import { dropRecording, recApi } from "@/lib/recordings";
import type { Recording } from "@/lib/recording-types";
import { clock, lengthLabel } from "@/lib/recording-types";
import type { Thread } from "@/lib/types";
import { cn } from "@/lib/utils";

const MAX_FILES = 10;
const MAX_FILE = 25 << 20;

export function fileSize(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  return `${Math.max(1, Math.round(n / 1024))} KB`;
}

export function ShareRecordingDialog({ rec, open, onOpenChange }: { rec: Recording; open: boolean; onOpenChange: (v: boolean) => void }) {
  const enabled = useChats((s) => s.enabled);
  const offMessage = useChats((s) => s.offMessage);
  const chats = useChats((s) => s.chats);
  const list = useMemo(() => sortedChats(chats).filter((c) => c.status === "active"), [chats]);
  const parts = rec.segments.filter((g) => g.bytes > 0);
  const [chatId, setChatId] = useState("");
  const [picked, setPicked] = useState<number[]>([]);
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    void loadChats();
    setBody("");
    setBusy(false);
    setPicked(parts.slice(0, MAX_FILES).map((g) => g.n));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- reset when the dialog opens
  }, [open, rec.id]);
  useEffect(() => {
    if (open && !chatId && list.length) setChatId(list[0].id);
  }, [open, chatId, list]);

  const chosen = parts.filter((g) => picked.includes(g.n));
  const total = chosen.reduce((a, g) => a + g.bytes, 0);
  const tooBig = chosen.filter((g) => g.bytes > MAX_FILE);
  const ok = !!chatId && chosen.length > 0 && chosen.length <= MAX_FILES && tooBig.length === 0;
  const chat = chats[chatId];

  async function share() {
    if (!ok || busy) return;
    setBusy(true);
    try {
      await recApi.share(rec.id, chatId, body.trim() || undefined, chosen.length === parts.length ? undefined : chosen.map((g) => g.n));
      onOpenChange(false);
      toast.success(`Shared in ${chat?.name ?? "the chat"}`, { action: { label: "Open", onClick: () => navigate(paths.chat(chatId)) } });
    } catch (e) {
      toast.error("Not shared", { description: (e as Error).message });
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !busy && onOpenChange(v)}>
      <DialogContent className="sm:max-w-[31.25rem]">
        <DialogHeader>
          <DialogTitle>Share to a chat</DialogTitle>
          <DialogDescription>
            {parts.length === 1 ? "The recording" : `Each part (${parts.length})`} goes into one message as a file. Everyone in the chat gets a copy.
          </DialogDescription>
        </DialogHeader>
        {enabled === false ? (
          <p className="rounded-md border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">{offMessage || "Chats with other people are off on this machine."}</p>
        ) : list.length === 0 ? (
          <p className="rounded-md border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">{enabled === null ? "Loading chats…" : "No chats yet. Invite someone from Chats first."}</p>
        ) : (
          <div className="grid gap-1.5">
            <Label>Chat</Label>
            <Select value={chatId} onValueChange={setChatId}>
              <SelectTrigger className="w-full" aria-label="Chat">
                <SelectValue placeholder="Pick a chat" />
              </SelectTrigger>
              <SelectContent>
                {list.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.kind === "group" && <Users className="size-3.5 text-muted-foreground" />}
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}
        {parts.length > 1 && (
          <div className="grid gap-1.5">
            <Label>Parts</Label>
            <div className="flex flex-wrap gap-1.5">
              {parts.map((g) => {
                const on = picked.includes(g.n);
                return (
                  <button
                    key={g.n}
                    type="button"
                    aria-pressed={on}
                    onClick={() => setPicked((p) => (on ? p.filter((x) => x !== g.n) : [...p, g.n].sort((a, b) => a - b)))}
                    className={cn(
                      "tabular rounded-md border px-2 py-1 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring",
                      on ? "border-border-strong bg-accent font-medium" : "text-muted-foreground",
                    )}
                  >
                    {g.n} · {clock(g.duration_ms)} · {fileSize(g.bytes)}
                  </button>
                );
              })}
            </div>
          </div>
        )}
        <div className="grid gap-1.5">
          <Label htmlFor="share-body">Message</Label>
          <Input id="share-body" value={body} onChange={(e) => setBody(e.target.value)} placeholder={`Recording: ${rec.title} (${lengthLabel(rec.duration_ms)})`} />
        </div>
        <div className="grid gap-1 text-xs text-muted-foreground">
          <p className="tabular">
            {chosen.length} {chosen.length === 1 ? "file" : "files"}, {fileSize(total)}
            {chat ? ` to ${chat.name}` : ""}
            {chat?.kind === "group" && chat.members ? ` (${chat.members.length} people)` : ""}.
          </p>
          {chosen.length > MAX_FILES && <p className="text-state-error">Too long to share in one message: pick at most {MAX_FILES} parts.</p>}
          {tooBig.length > 0 && <p className="text-state-error">Part {tooBig[0].n} is larger than 25 MB, the limit for a chat file.</p>}
          <p>Large files arrive only over a direct connection; through a relay only small ones pass.</p>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={busy}>
            Cancel
          </Button>
          <Button onClick={share} disabled={!ok || busy || enabled === false}>
            {busy && <Loader2 className="animate-spin" />}
            Share
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function BoardLinkDialog({ rec, open, onOpenChange }: { rec: Recording; open: boolean; onOpenChange: (v: boolean) => void }) {
  const board = useBoard((s) => s.board);
  const boardError = useBoard((s) => s.error);
  const threads = useMemo(() => board?.threads.filter((t) => !t.archived) ?? null, [board]);
  const [thread, setThread] = useState("general");
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    setText(`Recording: ${rec.title} (${lengthLabel(rec.duration_ms)})`);
    setError("");
    setBusy(false);
    void loadBoard(); // the topics, in the owner's order
  }, [open, rec.id, rec.title, rec.duration_ms]);

  async function add() {
    if (busy || !text.trim()) return;
    setBusy(true);
    try {
      await boardApi.addEntry({ thread, kind: "link", text: text.trim().slice(0, 400), link: `#/recordings/${rec.id}` });
      onOpenChange(false);
      toast.success("Linked on the Matrix", { action: { label: "Open", onClick: () => navigate(paths.matrix()) } });
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !busy && onOpenChange(v)}>
      <DialogContent className="sm:max-w-[28.75rem]">
        <DialogHeader>
          <DialogTitle>Add to Board</DialogTitle>
          <DialogDescription>A link line in a topic's log. The recording itself stays here.</DialogDescription>
        </DialogHeader>
        <div className="grid gap-1.5">
          <Label>Topic</Label>
          <Select value={thread} onValueChange={setThread}>
            <SelectTrigger className="w-full" aria-label="Topic">
              <SelectValue placeholder="General" />
            </SelectTrigger>
            <SelectContent>
              {(threads ?? [{ id: "general", title: "General" } as Thread]).map((t) => (
                <SelectItem key={t.id} value={t.id}>
                  {t.title}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="board-text">Line</Label>
          <Input id="board-text" value={text} onChange={(e) => setText(e.target.value)} maxLength={400} onKeyDown={(e) => e.key === "Enter" && void add()} />
        </div>
        {(error || (boardError && !threads)) && <p className="text-xs text-state-error">{error || boardError}</p>}
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={busy}>
            Cancel
          </Button>
          <Button onClick={add} disabled={busy || !text.trim()}>
            {busy && <Loader2 className="animate-spin" />}
            Add link
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function DeleteRecordingDialog({ rec, open, onOpenChange }: { rec: Recording; open: boolean; onOpenChange: (v: boolean) => void }) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete “{rec.title}”?</AlertDialogTitle>
          <AlertDialogDescription>
            Its {rec.segments.length === 1 ? "file is" : `${rec.segments.length} parts are`} removed from this machine ({fileSize(rec.bytes)}). Copies shared in chats stay
            there.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            className="bg-destructive text-white hover:bg-destructive/90"
            onClick={async () => {
              try {
                await recApi.remove(rec.id);
                dropRecording(rec.id);
                toast(`Deleted ${rec.title}`);
                navigate("/recordings");
              } catch (e) {
                toast.error((e as Error).message);
              }
            }}
          >
            Delete recording
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
