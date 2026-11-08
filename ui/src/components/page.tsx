import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/** Top bar of a page. Doubles as the window's drag region in the desktop app. */
export function PageHeader({ children, actions, className }: { children: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    // On a narrow screen at a large text size the actions wrap under the title rather than squeeze it
    // (lib/text-size.ts); a desktop keeps one row.
    <header className={cn("drag-region flex min-h-[3.25rem] shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b px-5 py-2 md:flex-nowrap md:px-6 md:py-0", className)}>
      <div className="flex min-w-0 flex-1 basis-48 items-center gap-2">{children}</div>
      {actions && <div className="flex shrink-0 flex-wrap items-center gap-1.5">{actions}</div>}
    </header>
  );
}

export function PageTitle({ children, className }: { children: ReactNode; className?: string }) {
  return <h1 className={cn("truncate text-[0.875rem] font-semibold tracking-tight", className)}>{children}</h1>;
}

export function PageBody({ children, className, width = "max-w-[65rem]" }: { children: ReactNode; className?: string; width?: string }) {
  return (
    // relative: anything absolutely positioned inside (sr-only text, popper anchors) stays in
    // this scroller instead of stretching the whole window, which then scrolled with it.
    <div className="scroll-thin relative min-h-0 min-w-0 flex-1 overflow-y-auto">
      <div className={cn("mx-auto min-w-0 w-full px-5 py-6 md:px-8 md:py-8", width, className)}>{children}</div>
    </div>
  );
}

export function Section({
  title,
  right,
  children,
  className,
}: {
  title: ReactNode;
  right?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section id={typeof title === "string" ? title.toLowerCase().replace(/[^a-z0-9]+/g, "-") : undefined} className={cn("min-w-0", className)}>
      <div className="mb-2.5 flex min-h-7 flex-wrap items-center gap-2">
        <h2 className="text-[0.8125rem] font-medium text-foreground">{title}</h2>
        {right && <div className="ml-auto flex items-center gap-1.5">{right}</div>}
      </div>
      {children}
    </section>
  );
}

export function EmptyState({
  icon: Icon,
  title,
  children,
  actions,
  className,
}: {
  icon: React.ComponentType<{ className?: string }>;
  title: string;
  children?: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col items-center rounded-xl border bg-card px-6 py-12 text-center", className)}>
      <div className="mb-4 flex size-10 items-center justify-center rounded-lg border bg-muted/50">
        <Icon className="size-[1.125rem] text-muted-foreground" />
      </div>
      <p className="text-[0.875rem] font-medium">{title}</p>
      {children && <div className="mt-1.5 max-w-sm text-sm text-muted-foreground">{children}</div>}
      {actions && <div className="mt-5 flex flex-wrap items-center justify-center gap-2">{actions}</div>}
    </div>
  );
}
