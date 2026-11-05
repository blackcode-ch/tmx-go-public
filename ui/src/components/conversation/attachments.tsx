import { useEffect, useState } from "react";
import { AlertCircle, FileText, ImageOff, Loader2 } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { uploadImageURL, type AttachedFile } from "@/lib/attachments";
import { cn } from "@/lib/utils";
import { CopyButton } from "../copy-button";

/** Loads an image by its file id and returns an object URL. */
type LoadImage = (id: string) => Promise<string>;

const loadUpload: LoadImage = uploadImageURL;

/** The files a message carried, under its bubble: image thumbnails (click to enlarge), file chips.
 * loadImage fetches an image's bytes (default: an agent message's upload). */
export function AttachmentList({ files, className, loadImage = loadUpload }: { files: AttachedFile[]; className?: string; loadImage?: LoadImage }) {
  return (
    <ul className={cn("flex flex-wrap justify-end gap-2", className)} aria-label="Attached files">
      {files.map((f) =>
        f.image && f.id && (f.state ?? "ready") === "ready" ? <ImageThumb key={f.id || f.path} f={f} load={loadImage} /> : <FileRow key={f.id || f.path} f={f} />,
      )}
    </ul>
  );
}

function ImageThumb({ f, load }: { f: AttachedFile; load: LoadImage }) {
  const [url, setUrl] = useState<string>();
  const [failed, setFailed] = useState(false);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    let live = true;
    load(f.id).then(
      (u) => live && setUrl(u),
      () => live && setFailed(true),
    );
    return () => {
      live = false;
    };
  }, [f.id, load]);
  if (failed) return <FileRow f={f} gone />;
  return (
    <li>
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-label={`Show ${f.name}`}
        className="block size-24 overflow-hidden rounded-xl border border-border bg-muted outline-none transition-opacity duration-100 hover:opacity-90 focus-visible:ring-2 focus-visible:ring-ring"
      >
        {url ? <img src={url} alt={f.name} className="size-full object-cover" /> : <span className="block size-full animate-pulse" />}
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-[min(92vw,1100px)] gap-3 p-3 sm:max-w-[min(92vw,1100px)]">
          <DialogTitle className="truncate pr-8 text-sm">{f.name}</DialogTitle>
          <DialogDescription className="sr-only">
            {f.type}, {f.size}
          </DialogDescription>
          {url && <img src={url} alt={f.name} className="max-h-[78vh] w-full rounded-lg object-contain" />}
          <PathLine f={f} />
        </DialogContent>
      </Dialog>
    </li>
  );
}

function FileRow({ f, gone }: { f: AttachedFile; gone?: boolean }) {
  const pending = f.state === "pending";
  const failed = f.state === "failed";
  const sub = gone ? "No longer stored" : pending ? `Downloading… · ${f.size}` : failed ? f.error || "Couldn't download" : f.size;
  return (
    <li
      className={cn(
        "flex h-12 max-w-64 min-w-0 items-center gap-2.5 rounded-xl border border-border bg-card py-1.5 pr-1.5 pl-2.5",
        !f.path && "pr-3",
      )}
    >
      <span className={cn("flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground", failed && "text-destructive")}>
        {gone ? (
          <ImageOff className="size-4" />
        ) : pending ? (
          <Loader2 className="size-4 animate-spin" />
        ) : failed ? (
          <AlertCircle className="size-4" />
        ) : (
          <FileText className="size-4" />
        )}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-xs font-medium text-foreground" title={f.path || f.name}>
          {f.name}
        </span>
        <span className={cn("block truncate text-2xs text-subtle-foreground", failed && "text-destructive")} title={failed ? f.error : undefined}>
          {sub}
        </span>
      </span>
      {f.path && <CopyButton text={f.path} label="Copy path" />}
    </li>
  );
}

function PathLine({ f }: { f: AttachedFile }) {
  return (
    <div className="flex min-w-0 items-center gap-1 text-2xs text-subtle-foreground">
      <span className="min-w-0 flex-1 truncate font-mono" title={f.path}>
        {f.path}
      </span>
      <span className="shrink-0">{f.size}</span>
      <CopyButton text={f.path} label="Copy path" />
    </div>
  );
}
