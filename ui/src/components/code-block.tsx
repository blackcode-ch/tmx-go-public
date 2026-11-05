import { useEffect, useState } from "react";
import { highlight } from "@/lib/highlight";
import { cn } from "@/lib/utils";
import { CopyButton } from "./copy-button";

export function useHighlighted(code: string, lang: string | undefined): string | null {
  const [html, setHtml] = useState<{ key: string; html: string | null }>({ key: "", html: null });
  const key = (lang ?? "") + "\0" + code;
  useEffect(() => {
    let live = true;
    highlight(code, lang)
      .then((h) => live && setHtml({ key, html: h }))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [code, lang, key]);
  return html.key === key ? html.html : null;
}

/** A code block with highlighting, a language label and a copy button. */
export function CodeBlock({
  code,
  lang,
  className,
  maxHeight = 420,
  header = true,
  label,
  wrap,
}: {
  code: string;
  lang?: string;
  className?: string;
  maxHeight?: number | "none";
  header?: boolean;
  label?: string;
  /** wrap long lines instead of scrolling (commands under review) */
  wrap?: boolean;
}) {
  const text = code.replace(/\n$/, "");
  const html = useHighlighted(text, lang);
  return (
    <div className={cn("group/code relative my-2 overflow-hidden rounded-lg border bg-code", className)}>
      {header && (
        <div className="flex h-7 items-center justify-between border-b px-2.5 pr-1">
          <span className="truncate font-mono text-2xs text-subtle-foreground">{label ?? (lang && lang !== "text" ? lang : "")}</span>
          <CopyButton text={text} label="Copy code" className="opacity-70 group-hover/code:opacity-100" />
        </div>
      )}
      <div
        className="scroll-thin overflow-auto px-3 py-2.5 font-mono text-[0.75rem] leading-[1.6]"
        style={{ maxHeight: maxHeight === "none" ? undefined : maxHeight }}
      >
        {html ? (
          <div className={cn("[&_pre]:m-0", wrap ? "[&_pre]:whitespace-pre-wrap [&_pre]:break-all" : "[&_pre]:whitespace-pre")} dangerouslySetInnerHTML={{ __html: html }} />
        ) : (
          <pre className={cn("m-0 text-foreground/90", wrap ? "whitespace-pre-wrap break-all" : "whitespace-pre")}>{text}</pre>
        )}
      </div>
      {!header && (
        <CopyButton text={text} label="Copy" className="absolute top-1 right-1 bg-code opacity-0 group-hover/code:opacity-100 focus-visible:opacity-100" />
      )}
    </div>
  );
}
