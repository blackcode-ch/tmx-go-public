// Small inputs of the profile editor: toggle chips, a token list, the instructions editor and
// its live preview.
import { useMemo, useRef, useState } from "react";
import { Check, Plus, X } from "lucide-react";
import { Textarea } from "@/components/ui/textarea";
import { MAIL_RULES, READ_ONLY_NOTE, TEMPLATE_VARS, templateParts } from "@/lib/profiles";
import { cn } from "@/lib/utils";

export function ToggleChip({
  pressed,
  onToggle,
  disabled,
  locked,
  title,
  children,
}: {
  pressed: boolean;
  onToggle: () => void;
  disabled?: boolean;
  /** shown as on and not clickable, without the disabled fade */
  locked?: boolean;
  title?: string;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      aria-disabled={locked || undefined}
      disabled={disabled}
      title={title}
      onClick={locked ? undefined : onToggle}
      className={cn(
        "inline-flex h-7 items-center gap-1.5 rounded-md border px-2.5 text-[0.7812rem] outline-none transition-colors duration-150 focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        locked && "cursor-default",
        pressed
          ? "border-brand/35 bg-brand-soft text-foreground"
          : "border-border text-muted-foreground hover:bg-muted/60 hover:text-foreground",
      )}
    >
      <span
        className={cn(
          "flex size-3.5 items-center justify-center rounded-[4px] border transition-colors duration-150",
          pressed ? "border-brand bg-brand text-brand-foreground" : "border-input bg-background",
        )}
      >
        {pressed && <Check className="size-2.5" strokeWidth={3} />}
      </span>
      {children}
    </button>
  );
}

/** A list of short strings (tool names, model IDs) edited as removable tokens. */
export function TokenField({
  value,
  onChange,
  placeholder,
  validate,
  id,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
  /** returns an error sentence, or "" */
  validate?: (s: string) => string;
  id?: string;
}) {
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  function add() {
    const s = text.trim();
    if (!s) return;
    const err = validate?.(s) ?? "";
    if (err) {
      setError(err);
      return;
    }
    if (!value.includes(s)) onChange([...value, s]);
    setText("");
    setError("");
  }
  return (
    <div>
      <div className="flex min-h-8 flex-wrap items-center gap-1.5 rounded-md border border-input px-1.5 py-1 focus-within:ring-2 focus-within:ring-ring dark:bg-input/20">
        {value.map((t) => (
          <span key={t} className="inline-flex h-6 max-w-full items-center gap-1 rounded-[5px] bg-muted pr-0.5 pl-2 font-mono text-[0.7188rem]">
            <span className="truncate">{t}</span>
            <button
              type="button"
              aria-label={`Remove ${t}`}
              onClick={() => onChange(value.filter((x) => x !== t))}
              className="flex size-5 items-center justify-center rounded text-muted-foreground outline-none hover:bg-background hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
            >
              <X className="size-3" />
            </button>
          </span>
        ))}
        <input
          id={id}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            setError("");
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === ",") {
              e.preventDefault();
              add();
            } else if (e.key === "Backspace" && !text && value.length) {
              onChange(value.slice(0, -1));
            }
          }}
          onBlur={add}
          placeholder={value.length ? "" : placeholder}
          spellCheck={false}
          className="h-6 min-w-[8.75rem] flex-1 bg-transparent px-1 font-mono text-[0.75rem] outline-none placeholder:font-sans placeholder:text-[0.7812rem] placeholder:text-muted-foreground"
        />
        {text.trim() && (
          <button
            type="button"
            onMouseDown={(e) => e.preventDefault()}
            onClick={add}
            className="inline-flex h-6 items-center gap-1 rounded px-1.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            <Plus className="size-3" /> Add
          </button>
        )}
      </div>
      {error && <p className="mt-1.5 text-xs text-state-error">{error}</p>}
    </div>
  );
}

/** The instructions template: a monospace editor with variable buttons that insert at the cursor. */
export function InstructionsEditor({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const ref = useRef<HTMLTextAreaElement>(null);
  function insert(name: string) {
    const el = ref.current;
    const token = `{{${name}}}`;
    if (!el) return onChange(value + token);
    const start = el.selectionStart ?? value.length;
    const end = el.selectionEnd ?? value.length;
    onChange(value.slice(0, start) + token + value.slice(end));
    requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(start + token.length, start + token.length);
    });
  }
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <Textarea
        ref={ref}
        aria-label="Instructions template"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        spellCheck={false}
        className="min-h-[18.75rem] resize-y bg-card font-mono text-[0.7812rem] leading-[1.6] dark:bg-card"
      />
      <div className="flex flex-wrap items-center gap-1">
        <span className="mr-1 text-2xs text-muted-foreground">Insert</span>
        {TEMPLATE_VARS.map((v) => (
          <button
            key={v.name}
            type="button"
            title={v.hint}
            onClick={() => insert(v.name)}
            className="inline-flex h-6 items-center rounded-[5px] border px-1.5 font-mono text-[0.6875rem] text-muted-foreground outline-none transition-colors duration-150 hover:border-brand/35 hover:bg-brand-soft hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
          >
            {`{{${v.name}}}`}
          </button>
        ))}
      </div>
    </div>
  );
}

/** The rendered system prompt for an example agent: filled variables marked, tmx's fixed part below. */
export function InstructionsPreview({
  template,
  vars,
  readOnly,
  role,
  brief,
  header,
}: {
  template: string;
  vars: Record<string, string>;
  readOnly: boolean;
  role: string;
  brief: string;
  header: React.ReactNode;
}) {
  const parts = useMemo(() => templateParts(template.trim()), [template]);
  const fixed = [readOnly ? READ_ONLY_NOTE : "", role ? `Your role: ${role}` : "", brief ? `Your brief: ${brief}` : "", MAIL_RULES].filter(Boolean);
  return (
    <div className="flex min-w-0 flex-col overflow-hidden rounded-md border bg-muted/30">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b bg-card px-3">{header}</div>
      <div className="scroll-thin max-h-[35rem] min-h-[18.75rem] flex-1 overflow-y-auto px-3.5 py-3 font-mono text-[0.75rem] leading-[1.6] break-words whitespace-pre-wrap">
        {parts.length === 0 && <span className="text-subtle-foreground">(no instructions)</span>}
        {parts.map((p, i) =>
          p.v === undefined ? (
            <span key={i}>{p.text}</span>
          ) : p.known ? (
            <span key={i} className="rounded-[3px] bg-brand-soft text-brand" title={`{{${p.v}}}`}>
              {vars[p.v] ?? p.text}
            </span>
          ) : (
            <span key={i} className="rounded-[3px] bg-diff-del-bg text-state-error" title="Unknown variable: kept as written">
              {p.text}
            </span>
          ),
        )}
        <div className="mt-3 border-t border-dashed pt-2.5 text-muted-foreground">
          <div className="mb-1 font-sans text-2xs font-medium tracking-wide text-subtle-foreground uppercase">Added by tmx</div>
          {fixed.join("\n")}
        </div>
      </div>
    </div>
  );
}
