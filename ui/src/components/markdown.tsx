import { memo, type ReactElement, type ReactNode } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";
import { CodeBlock } from "./code-block";

function textOf(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  if (node && typeof node === "object" && "props" in node) return textOf((node as ReactElement<{ children?: ReactNode }>).props.children);
  return "";
}

const components: Components = {
  pre({ children }) {
    const child = (Array.isArray(children) ? children[0] : children) as ReactElement<{ className?: string; children?: ReactNode }>;
    const cls = child?.props?.className ?? "";
    const lang = /language-([\w+-]+)/.exec(cls)?.[1];
    return <CodeBlock code={textOf(child?.props?.children)} lang={lang} />;
  },
  a({ href, children }) {
    // @mentions arrive as links to "#@me" / "#@mention" (see lib/mentions.ts).
    if (href === "#@me" || href === "#@mention") {
      return <span className={cn("mention", href === "#@me" && "mention-me")}>{children}</span>;
    }
    return (
      <a
        href={href}
        target="_blank"
        rel="noreferrer noopener"
        onClick={(e) => {
          const t = useStore.getState().transport;
          if (href && t?.desktop) {
            e.preventDefault();
            t.openExternal(href);
          }
        }}
      >
        {children}
      </a>
    );
  },
};

export const Markdown = memo(function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={cn("prose-tmx", className)}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  );
});
