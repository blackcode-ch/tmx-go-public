// Dialogs of the board canvas: a new topic (title and colour) and renaming one.
import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { colorValue, THREAD_COLORS } from "@/lib/board";
import { cn } from "@/lib/utils";

export function NewTopicDialog({
  open,
  onOpenChange,
  description,
  create,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  description: string;
  /** opens the topic (and does what follows); a thrown error is shown */
  create: (title: string, color: string) => Promise<unknown>;
}) {
  const [title, setTitle] = useState("");
  const [color, setColor] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setTitle("");
      setColor("");
    }
  }, [open]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!title.trim()) return;
    setBusy(true);
    try {
      await create(title.trim(), color);
      onOpenChange(false);
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>New topic</DialogTitle>
            <DialogDescription>{description}</DialogDescription>
          </DialogHeader>
          <div className="mt-4 space-y-4">
            <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="e.g. Launch plan" maxLength={80} autoFocus aria-label="Topic title" />
            <div className="flex flex-wrap items-center gap-1.5" role="radiogroup" aria-label="Colour">
              <ColorSwatch value="" current={color} onPick={setColor} />
              {THREAD_COLORS.map((c) => (
                <ColorSwatch key={c} value={c} current={color} onPick={setColor} />
              ))}
            </div>
          </div>
          <DialogFooter className="mt-6">
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={!title.trim() || busy}>
              Create topic
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ColorSwatch({ value, current, onPick }: { value: string; current: string; onPick: (c: string) => void }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={current === value}
      aria-label={value || "Automatic"}
      title={value || "Automatic"}
      onClick={() => onPick(value)}
      className={cn(
        "size-6 rounded-full border-2 border-transparent ring-offset-2 ring-offset-background transition-shadow",
        current === value && "ring-2 ring-ring",
        !value && "border-dashed border-muted-foreground",
      )}
      style={value ? { background: colorValue(value) } : undefined}
    />
  );
}

export function RenameDialog({ open, onOpenChange, title, onSave }: { open: boolean; onOpenChange: (v: boolean) => void; title: string; onSave: (t: string) => Promise<void> }) {
  const [v, setV] = useState(title);
  useEffect(() => {
    if (open) setV(title);
  }, [open, title]);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (!v.trim()) return;
            void onSave(v.trim()).then(() => onOpenChange(false));
          }}
        >
          <DialogHeader>
            <DialogTitle>Rename topic</DialogTitle>
          </DialogHeader>
          <Input className="mt-4" value={v} onChange={(e) => setV(e.target.value)} maxLength={80} autoFocus aria-label="Topic title" />
          <DialogFooter className="mt-6">
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={!v.trim() || v.trim() === title}>
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
