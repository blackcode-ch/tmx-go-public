import { CheckCircle2, Circle, CircleDot } from "lucide-react";
import { asInput, langFromPath } from "@/lib/tools";
import { relPath } from "@/lib/format";
import { cn } from "@/lib/utils";
import { CodeBlock } from "../code-block";
import { DiffView } from "../diff-view";

function str(v: unknown): string {
  return typeof v === "string" ? v : v == null ? "" : JSON.stringify(v, null, 2);
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 items-baseline gap-2 text-xs">
      <span className="w-16 shrink-0 text-subtle-foreground">{label}</span>
      <span className="min-w-0 font-mono break-all text-foreground/85">{children}</span>
    </div>
  );
}

/** The interesting part of a tool call's input, rendered for humans. */
export function ToolInputView({ tool, input, cwd, compact, hidePath }: { tool?: string; input: unknown; cwd?: string; compact?: boolean; hidePath?: boolean }) {
  const showPath = !compact && !hidePath;
  const i = asInput(input);
  switch (tool) {
    case "Bash":
      return (
        <div className="space-y-1.5">
          <CodeBlock code={str(i.command)} lang="bash" header={false} className="my-0" maxHeight={compact ? 200 : 320} wrap />
          {typeof i.timeout === "number" && <Field label="timeout">{Math.round(i.timeout / 1000)}s</Field>}
        </div>
      );
    case "Edit":
      return (
        <div className="space-y-1.5">
          {showPath && <Field label="file">{relPath(str(i.file_path), cwd)}</Field>}
          <DiffView oldStr={str(i.old_string)} newStr={str(i.new_string)} maxHeight={compact ? 260 : 420} />
          {i.replace_all === true && <Field label="mode">replace all occurrences</Field>}
        </div>
      );
    case "MultiEdit":
      return (
        <div className="space-y-1.5">
          {showPath && <Field label="file">{relPath(str(i.file_path), cwd)}</Field>}
          {(Array.isArray(i.edits) ? i.edits : []).map((e: Record<string, unknown>, k: number) => (
            <DiffView key={k} oldStr={str(e.old_string)} newStr={str(e.new_string)} maxHeight={240} />
          ))}
        </div>
      );
    case "Write":
      return (
        <div className="space-y-1.5">
          {showPath && <Field label="file">{relPath(str(i.file_path), cwd)}</Field>}
          <CodeBlock code={str(i.content)} lang={langFromPath(str(i.file_path))} label={relPath(str(i.file_path), cwd)} className="my-0" maxHeight={compact ? 240 : 380} />
        </div>
      );
    case "Read":
      return (
        <div className="space-y-1">
          {!hidePath && <Field label="file">{relPath(str(i.file_path), cwd)}</Field>}
          {(i.offset != null || i.limit != null) && (
            <Field label="lines">
              {String(i.offset ?? 1)}
              {i.limit != null ? `–${Number(i.offset ?? 1) + Number(i.limit) - 1}` : "+"}
            </Field>
          )}
        </div>
      );
    case "Grep":
      return (
        <div className="space-y-1">
          <Field label="pattern">{str(i.pattern)}</Field>
          {i.path != null && <Field label="in">{relPath(str(i.path), cwd)}</Field>}
          {i.glob != null && <Field label="glob">{str(i.glob)}</Field>}
          {i.output_mode != null && <Field label="output">{str(i.output_mode)}</Field>}
        </div>
      );
    case "Glob":
      return (
        <div className="space-y-1">
          <Field label="pattern">{str(i.pattern)}</Field>
          {i.path != null && <Field label="in">{relPath(str(i.path), cwd)}</Field>}
        </div>
      );
    case "WebFetch":
      return (
        <div className="space-y-1">
          <Field label="url">{str(i.url)}</Field>
          {i.prompt != null && <p className="text-xs text-muted-foreground">{str(i.prompt)}</p>}
        </div>
      );
    case "TodoWrite": {
      const todos = Array.isArray(i.todos) ? (i.todos as Record<string, unknown>[]) : [];
      return (
        <ul className="space-y-1">
          {todos.map((t, k) => {
            const st = str(t.status);
            const Icon = st === "completed" ? CheckCircle2 : st === "in_progress" ? CircleDot : Circle;
            return (
              <li key={k} className={cn("flex items-start gap-2 text-sm", st === "completed" && "text-muted-foreground line-through")}>
                <Icon className={cn("mt-0.5 size-3.5 shrink-0", st === "in_progress" ? "text-state-working" : "text-subtle-foreground")} />
                <span>{str(t.content)}</span>
              </li>
            );
          })}
        </ul>
      );
    }
  }
  if (tool === "mcp__tmxg__whatsapp_send") {
    // the line above names the chat; here only the words that would go out
    return <div className="rounded-md border-l-2 bg-muted/40 px-3 py-2 text-sm whitespace-pre-wrap">{str(i.text ?? i.body)}</div>;
  }
  if (tool === "mcp__tmxg__send" || tool === "mcp__tmxg__report" || tool === "mcp__tmxg__reply") {
    return (
      <div className="space-y-1.5">
        {i.to != null && <Field label="to">{str(i.to)}</Field>}
        <div className="rounded-md border-l-2 bg-muted/40 px-3 py-2 text-sm whitespace-pre-wrap">{str(i.body)}</div>
      </div>
    );
  }
  const keys = Object.keys(i);
  if (keys.length === 0) return <p className="text-xs text-subtle-foreground">No input.</p>;
  if (keys.every((k) => typeof i[k] !== "object" || i[k] === null) && keys.length <= 6) {
    return (
      <div className="space-y-1">
        {keys.map((k) => (
          <Field key={k} label={k}>
            {str(i[k])}
          </Field>
        ))}
      </div>
    );
  }
  return <CodeBlock code={JSON.stringify(i, null, 2)} lang="json" header={false} className="my-0" maxHeight={260} />;
}
