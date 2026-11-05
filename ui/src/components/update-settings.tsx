// Settings → Updates: the version, what the updater is doing, auto/channel, check/install now,
// and a GitHub token when the release repository is private.
import { useEffect, useState } from "react";
import { ArrowUpCircle, CheckCircle2, ExternalLink, Loader2, RefreshCw, TriangleAlert } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Section } from "@/components/page";
import { relativeTime } from "@/lib/format";
import { useStore } from "@/lib/store";
import { loadUpdate, updateApi, useUpdate, type UpdateStatus } from "@/lib/update";
import { cn } from "@/lib/utils";

function Row({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-2 py-3 sm:flex-row sm:items-start sm:gap-6">
      <div className="min-w-0 sm:w-56 sm:shrink-0 sm:pt-1.5">
        <div className="text-sm">{label}</div>
        {hint && <div className="mt-0.5 text-xs text-muted-foreground">{hint}</div>}
      </div>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

function headline(s: UpdateStatus): { icon: React.ReactNode; text: string; tone?: "warn" | "ok" | "new" } {
  switch (s.state) {
    case "off":
      return { icon: <TriangleAlert className="size-4" />, text: s.reason ?? "This build does not update itself.", tone: "warn" };
    case "checking":
      return { icon: <Loader2 className="size-4 animate-spin" />, text: "Checking for a new release…" };
    case "available":
      return { icon: <ArrowUpCircle className="size-4" />, text: `${s.latest} is available.`, tone: "new" };
    case "downloading":
      return { icon: <Loader2 className="size-4 animate-spin" />, text: `Downloading and checking ${s.latest}…`, tone: "new" };
    case "ready":
      return {
        icon: <ArrowUpCircle className="size-4" />,
        text: `${s.latest} is downloaded and checked. It installs once ${s.waiting?.length ? s.waiting.join(", ") : "the agents"} ${s.waiting && s.waiting.length === 1 ? "is" : "are"} done.`,
        tone: "new",
      };
    case "installing":
      return { icon: <Loader2 className="size-4 animate-spin" />, text: `Installing ${s.latest}; tmx restarts on it and the agents resume.`, tone: "new" };
    case "error":
      return { icon: <TriangleAlert className="size-4" />, text: s.error ?? "The last check failed.", tone: "warn" };
    default:
      return s.latest
        ? { icon: <CheckCircle2 className="size-4" />, text: "tmx is up to date.", tone: "ok" }
        : { icon: <CheckCircle2 className="size-4" />, text: "Not checked yet." };
  }
}

export function UpdateSection() {
  const s = useUpdate((x) => x.status);
  const status = useStore((x) => x.status);
  const [busy, setBusy] = useState<string | null>(null);
  const [token, setToken] = useState("");

  useEffect(() => {
    if (status === "ready") void loadUpdate();
  }, [status]);

  if (!s) return null; // a daemon without the updater

  async function run(what: string, fn: () => Promise<unknown>, done?: string) {
    setBusy(what);
    try {
      await fn();
      if (done) toast.success(done);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(null);
    }
  }

  const h = headline(s);
  const off = s.state === "off";
  const canInstall = s.available && ["available", "ready", "error"].includes(s.state);
  return (
    <div id="updates" className="scroll-mt-6">
      <Section title="Updates">
        <div className="divide-y rounded-xl border bg-card px-4">
          <div className="flex flex-col gap-3 py-3.5 sm:flex-row sm:items-center">
            <div
              className={cn(
                "flex min-w-0 flex-1 items-start gap-2.5 text-sm",
                h.tone === "warn" && "text-state-waiting",
                h.tone === "new" && "text-brand",
                h.tone === "ok" && "text-foreground",
              )}
            >
              <span className="mt-0.5 shrink-0">{h.icon}</span>
              <div className="min-w-0">
                <p className="[overflow-wrap:anywhere]">{h.text}</p>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  This is tmx <span className="font-mono">{s.current}</span>
                  {s.checked_at ? ` · checked ${relativeTime(s.checked_at)}` : ""}
                  {s.latest_url && s.available && (
                    <>
                      {" · "}
                      <a href={s.latest_url} target="_blank" rel="noreferrer noopener" className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline">
                        what's new <ExternalLink className="size-3" />
                      </a>
                    </>
                  )}
                </p>
              </div>
            </div>
            {!off && (
              <div className="flex shrink-0 flex-wrap items-center gap-1.5">
                {canInstall && (
                  <Button size="sm" disabled={!!busy} onClick={() => void run("install", () => updateApi.install(true))}>
                    {busy === "install" ? <Loader2 className="animate-spin" /> : <ArrowUpCircle />}
                    Update now
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="outline"
                  disabled={!!busy || s.state === "checking" || s.state === "downloading" || s.state === "installing"}
                  onClick={() => void run("check", () => updateApi.check())}
                >
                  {busy === "check" ? <Loader2 className="animate-spin" /> : <RefreshCw />}
                  Check now
                </Button>
              </div>
            )}
          </div>
          {canInstall && (
            <p className="py-2.5 text-xs text-muted-foreground">
              Update now restarts tmx at once: an agent in the middle of a step loses that step (it keeps its conversation and goes on after the
              restart). {s.auto ? "Automatic updates wait until no agent is busy." : ""}
            </p>
          )}
          {!off && (
            <>
              <Row label="Install updates automatically" hint="New releases download, are checked against tmx's signature, and install once no agent is busy.">
                <Switch
                  checked={s.auto}
                  disabled={!!busy}
                  onCheckedChange={(v) => void run("auto", () => updateApi.configure({ auto: v }))}
                  aria-label="Install updates automatically"
                  className="mt-1"
                />
              </Row>
              <Row label="Channel" hint="Stable: fleet releases. Latest: also includes fleet prereleases.">
                <Select value={s.channel} onValueChange={(v) => void run("channel", () => updateApi.configure({ channel: v as "stable" | "latest" }))}>
                  <SelectTrigger className="w-56" aria-label="Update channel">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="stable">Stable releases</SelectItem>
                    <SelectItem value="latest">Latest build of main</SelectItem>
                  </SelectContent>
                </Select>
              </Row>
              {(s.needs_token || s.has_token) && (
                <Row
                  label="GitHub token"
                  hint={`${s.repo} is private: tmx uses the GitHub CLI's sign-in (gh auth login) or a token with read access to it. Stored on this machine only.`}
                >
                  <form
                    className="flex max-w-md items-center gap-2"
                    onSubmit={(e) => {
                      e.preventDefault();
                      const tok = token.trim();
                      // Cleared once saved: a cancelled confirmation keeps it for another try.
                      if (tok)
                        void run(
                          "token",
                          async () => {
                            await updateApi.setToken(tok);
                            setToken("");
                            await updateApi.check();
                          },
                          "Token saved.",
                        );
                    }}
                  >
                    <Input
                      type="password"
                      autoComplete="off"
                      value={token}
                      onChange={(e) => setToken(e.target.value)}
                      placeholder={s.has_token ? "A token is saved" : "github_pat_…"}
                      aria-label="GitHub token"
                    />
                    <Button type="submit" size="sm" variant="outline" disabled={!token.trim() || !!busy}>
                      Save
                    </Button>
                    {s.has_token && (
                      <Button type="button" size="sm" variant="ghost" disabled={!!busy} onClick={() => void run("token", () => updateApi.setToken(""), "Token removed.")}>
                        Remove
                      </Button>
                    )}
                  </form>
                </Row>
              )}
            </>
          )}
        </div>
      </Section>
    </div>
  );
}
