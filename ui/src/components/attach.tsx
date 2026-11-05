// Attaching files to a message box: paperclip, paste and drag-and-drop, with chips that show
// each file uploading, ready or failed. Used by the agent composer and the chat composer;
// each passes its own upload call (files upload as soon as they are added, so sending is fast).
import { useEffect, useRef, useState, type DragEvent } from "react";
import { FileText, Loader2, Paperclip, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { formatSize, readBase64 } from "@/lib/attachments";
import { cn } from "@/lib/utils";

export interface Pending {
  key: string;
  name: string;
  size: number;
  image: boolean;
  preview?: string; // object URL of a local image
  status: "uploading" | "ready" | "error";
  id?: string; // the upload's id once stored
  error?: string;
}

export type UploadFn = (f: { name: string; type: string; data: string }) => Promise<{ id: string }>;

// Pending files per box (e.g. "agent:<id>", "chat:<id>"), kept while you switch pages.
const pendingByBox = new Map<string, Pending[]>();
const listeners = new Map<string, Set<(l: Pending[]) => void>>();
let seq = 0;

function change(box: string, fn: (l: Pending[]) => Pending[]) {
  const next = fn(pendingByBox.get(box) ?? []);
  if (next.length) pendingByBox.set(box, next);
  else pendingByBox.delete(box);
  listeners.get(box)?.forEach((l) => l(next));
}

function pastedName(file: File): string {
  if (file.name && file.name !== "image.png") return file.name;
  const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-");
  return `pasted-${stamp}.${file.type.split("/")[1] || "png"}`;
}

/** Files waiting to go with the next message of one box. */
export function useAttachments(box: string, upload: UploadFn, opts: { maxFiles: number; maxBytes: number; disabled?: boolean }) {
  const [files, setFiles] = useState<Pending[]>(() => pendingByBox.get(box) ?? []);
  const [dragging, setDragging] = useState(false);
  const up = useRef(upload);
  up.current = upload;

  useEffect(() => {
    setFiles(pendingByBox.get(box) ?? []);
    let set = listeners.get(box);
    if (!set) listeners.set(box, (set = new Set()));
    set.add(setFiles);
    return () => {
      set.delete(setFiles);
    };
  }, [box]);

  function add(list: FileList | File[]) {
    if (opts.disabled) return;
    const room = opts.maxFiles - (pendingByBox.get(box)?.length ?? 0);
    const all = [...list];
    if (all.length > room) toast.error(`A message carries at most ${opts.maxFiles} files.`);
    for (const file of all.slice(0, Math.max(room, 0))) {
      if (file.size > opts.maxBytes) {
        toast.error(`${file.name} is larger than ${opts.maxBytes >> 20} MB.`);
        continue;
      }
      if (file.size === 0) {
        toast.error(`${file.name || "The file"} is empty.`);
        continue;
      }
      const image = file.type.startsWith("image/");
      const name = pastedName(file);
      const p: Pending = { key: `f${++seq}`, name, size: file.size, image, status: "uploading", preview: image ? URL.createObjectURL(file) : undefined };
      change(box, (l) => [...l, p]);
      void readBase64(file)
        .then((data) => up.current({ name, type: file.type, data }))
        .then((r) => change(box, (l) => l.map((x) => (x.key === p.key ? { ...x, status: "ready", id: r.id } : x))))
        .catch((e: Error) => change(box, (l) => l.map((x) => (x.key === p.key ? { ...x, status: "error", error: e.message || "Upload failed." } : x))));
    }
  }

  function remove(key: string) {
    change(box, (l) => {
      const f = l.find((x) => x.key === key);
      if (f?.preview) URL.revokeObjectURL(f.preview);
      return l.filter((x) => x.key !== key);
    });
  }

  function clear() {
    change(box, (l) => {
      for (const f of l) if (f.preview) URL.revokeObjectURL(f.preview);
      return [];
    });
  }

  const dropProps = {
    onDragOver: (e: DragEvent) => {
      if (opts.disabled || !e.dataTransfer.types.includes("Files")) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "copy";
      setDragging(true);
    },
    onDragLeave: (e: DragEvent) => {
      if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false);
    },
    onDrop: (e: DragEvent) => {
      if (opts.disabled || !e.dataTransfer.files.length) return;
      e.preventDefault();
      setDragging(false);
      add(e.dataTransfer.files);
    },
  };

  const uploading = files.some((f) => f.status === "uploading");
  const readyIds = files.filter((f) => f.status === "ready" && f.id).map((f) => f.id!);
  return { files, add, remove, clear, uploading, readyIds, dragging, dropProps };
}

export function DropOverlay({ show }: { show: boolean }) {
  if (!show) return null;
  return (
    <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-xl bg-card/90 text-sm font-medium text-foreground">
      <Paperclip className="mr-2 size-4 text-muted-foreground" />
      Drop to attach
    </div>
  );
}

export function AttachButton({ onFiles, disabled, hint }: { onFiles: (f: FileList) => void; disabled?: boolean; hint?: string }) {
  const picker = useRef<HTMLInputElement>(null);
  return (
    <>
      <input
        ref={picker}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          if (e.target.files?.length) onFiles(e.target.files);
          e.target.value = "";
        }}
      />
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => picker.current?.click()}
            disabled={disabled}
            aria-label="Attach files"
            className="shrink-0 rounded-lg text-muted-foreground hover:text-foreground"
          >
            <Paperclip className="size-4" />
          </Button>
        </TooltipTrigger>
        <TooltipContent>{hint ?? "Attach files · or paste / drop them here"}</TooltipContent>
      </Tooltip>
    </>
  );
}

export function AttachChips({ files, onRemove }: { files: Pending[]; onRemove: (key: string) => void }) {
  if (!files.length) return null;
  return (
    <ul className="flex flex-wrap gap-2 px-3 pt-3" aria-label="Attached files">
      {files.map((f) => (
        <FileChip key={f.key} f={f} onRemove={() => onRemove(f.key)} />
      ))}
    </ul>
  );
}

function FileChip({ f, onRemove }: { f: Pending; onRemove: () => void }) {
  const remove = (
    <button
      type="button"
      onClick={onRemove}
      aria-label={`Remove ${f.name}`}
      className="absolute -top-1.5 -right-1.5 flex size-5 items-center justify-center rounded-full border border-border-strong bg-card text-muted-foreground opacity-0 shadow-sm transition-opacity duration-100 group-hover/chip:opacity-100 hover:text-foreground focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none [@media(hover:none)]:opacity-100"
    >
      <X className="size-3" />
    </button>
  );
  const chip =
    f.image && f.preview ? (
      <li className={cn("group/chip relative size-14 shrink-0 rounded-lg border border-border bg-muted", f.status === "error" && "border-destructive")}>
        <img src={f.preview} alt={f.name} className="size-full rounded-[inherit] object-cover" />
        {f.status === "uploading" && (
          <span className="absolute inset-0 flex items-center justify-center rounded-[inherit] bg-card/60">
            <Loader2 className="size-4 animate-spin text-muted-foreground" />
          </span>
        )}
        {remove}
      </li>
    ) : (
      <li
        className={cn(
          "group/chip relative flex h-14 max-w-56 min-w-0 items-center gap-2.5 rounded-lg border border-border bg-muted/50 py-2 pr-3 pl-2.5",
          f.status === "error" && "border-destructive",
        )}
      >
        <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-card text-muted-foreground">
          {f.status === "uploading" ? <Loader2 className="size-4 animate-spin" /> : <FileText className="size-4" />}
        </span>
        <span className="min-w-0">
          <span className="block truncate text-xs font-medium text-foreground">{f.name}</span>
          <span className={cn("block truncate text-2xs", f.status === "error" ? "text-destructive" : "text-subtle-foreground")}>
            {f.status === "error" ? "Upload failed" : formatSize(f.size)}
          </span>
        </span>
        {remove}
      </li>
    );
  if (f.status !== "error") return chip;
  return (
    <Tooltip>
      <TooltipTrigger asChild>{chip}</TooltipTrigger>
      <TooltipContent>{f.error}</TooltipContent>
    </Tooltip>
  );
}
