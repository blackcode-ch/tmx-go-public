import { useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import { displayPath, modelName, slugify } from "@/lib/format";
import { createProjectPayload, projectLeadHarnesses } from "@/lib/project-create";
import { navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import type { Harness, HarnessInfo } from "@/lib/types";
import { ui, useUI } from "@/lib/ui";

export function NewProjectDialog() {
  const open = useUI((s) => s.newProject);
  const workspace = useStore((s) => s.workspace);
  const leadModel = useStore((s) => s.policy.levels.lead?.model);
  const [name, setName] = useState("");
  const [folder, setFolder] = useState("");
  const [folderEdited, setFolderEdited] = useState(false);
  const [brief, setBrief] = useState("");
  const [harnesses, setHarnesses] = useState<HarnessInfo[]>([]);
  const [harnessesLoading, setHarnessesLoading] = useState(false);
  const [harnessLoadError, setHarnessLoadError] = useState(false);
  const [harness, setHarness] = useState<Harness>("claude");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (open) {
      setName("");
      setFolder("");
      setFolderEdited(false);
      setBrief("");
      setHarness("claude");
      setHarnesses([]);
      setHarnessLoadError(false);
      setHarnessesLoading(true);
      setError("");
      setBusy(false);
      let live = true;
      api.harnesses().then((hs) => {
        if (!live) return;
        const available = projectLeadHarnesses(hs ?? []);
        setHarnesses(available);
        const preferred = available.find((h) => h.name === "claude" && h.installed)
          ?? available.find((h) => h.installed)
          ?? available[0];
        if (preferred) setHarness(preferred.name);
        setHarnessesLoading(false);
      }).catch(() => {
        if (!live) return;
        setHarnessLoadError(true);
        setHarnessesLoading(false);
      });
      return () => { live = false; };
    }
  }, [open]);

  const selectedHarness = harnesses.find((h) => h.name === harness);

  const slug = slugify(name);
  const sep = workspace.includes("\\") && !workspace.includes("/") ? "\\" : "/";
  const defaultFolder = workspace && slug ? `${workspace.replace(/[\\/]$/, "")}${sep}${slug}` : "";
  const shownFolder = folderEdited ? folder : defaultFolder;

  async function create(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim() || !selectedHarness?.installed || harnessesLoading || busy) return;
    setBusy(true);
    setError("");
    try {
      const p = await api.createProject(createProjectPayload({
        name: name.trim(),
        harness,
        folder: folderEdited && folder.trim() && folder.trim() !== defaultFolder ? folder.trim() : undefined,
        brief: brief.trim() || undefined,
      }));
      ui.openNewProject(false);
      toast.success(`Created ${p.name}`, { description: "Its lead is starting." });
      navigate(paths.project(p.id));
    } catch (err) {
      setError((err as Error).message || "The project wasn't created.");
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !busy && ui.openNewProject(v)}>
      <DialogContent className="sm:max-w-[32.5rem]">
        <form onSubmit={create} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>New project</DialogTitle>
            <DialogDescription>
              A lead{harness === "claude" && leadModel ? ` on ${modelName(leadModel)}` : ""} starts in the project folder and staffs it with workers.
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="np-name">Name</Label>
            <Input id="np-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="billing-portal" autoFocus autoComplete="off" spellCheck={false} />
          </div>
          <div className="grid gap-1.5">
            <span className="text-sm font-medium">Lead harness</span>
            <div role="radiogroup" aria-label="Lead harness" className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              {harnessesLoading && <p className="col-span-full text-xs text-muted-foreground">Checking installed chat harnesses…</p>}
              {!harnessesLoading && harnessLoadError && <p className="col-span-full text-xs text-state-error">Couldn’t load installed harnesses. Reopen the dialog to try again.</p>}
              {!harnessesLoading && !harnessLoadError && harnesses.length === 0 && <p className="col-span-full text-xs text-muted-foreground">No installed chat harnesses are available for a project lead.</p>}
              {!harnessesLoading && !harnessLoadError && harnesses.length > 0 && !harnesses.some((h) => h.installed) && <p className="col-span-full text-xs text-muted-foreground">Install a chat harness before creating a project lead.</p>}
              {harnesses.map((h) => (
                <button
                  key={h.name}
                  type="button"
                  role="radio"
                  aria-checked={h.name === harness}
                  disabled={!h.installed || busy || harnessesLoading}
                  title={h.installed ? h.path : `Not installed here${h.install ? `: ${h.install}` : ""}`}
                  onClick={() => setHarness(h.name)}
                  className={`flex min-w-0 flex-col items-start gap-1 rounded-lg border px-3 py-2 text-left text-[0.8125rem] transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${h.name === harness ? "border-brand/50 bg-brand-soft" : "border-border hover:bg-muted/50"}`}
                >
                  <span className="font-medium">{h.label}</span>
                  <span className="text-2xs text-muted-foreground">{h.installed ? "Installed" : "Not installed"}</span>
                </button>
              ))}
            </div>
            <p className="text-2xs text-muted-foreground">The selected harness starts the project lead.</p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="np-folder">Folder</Label>
            <Input
              id="np-folder"
              value={shownFolder}
              onChange={(e) => {
                setFolderEdited(true);
                setFolder(e.target.value);
              }}
              placeholder={workspace ? `${displayPath(workspace)}/…` : "/path/to/project"}
              className="font-mono text-[0.7812rem]"
              spellCheck={false}
            />
            <p className="text-2xs text-muted-foreground">Created if it doesn't exist. Deleting the project later never deletes the folder.</p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="np-brief">Brief</Label>
            <Textarea
              id="np-brief"
              value={brief}
              onChange={(e) => setBrief(e.target.value)}
              placeholder="What should this project achieve?"
              className="min-h-24 resize-none"
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void create(e);
              }}
            />
          </div>
          {error && <p className="rounded-md border border-state-error/30 bg-diff-del-bg px-3 py-2 text-xs text-foreground">{error}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => ui.openNewProject(false)} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" disabled={!name.trim() || !selectedHarness?.installed || harnessesLoading || busy}>
              {busy && <Loader2 className="animate-spin" />}
              Create project
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
