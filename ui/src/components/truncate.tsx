import { useLayoutEffect, useRef, useState, type ElementType, type ReactNode } from "react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

/** Single-line text that shows the full value in a tooltip only when it is cut off. */
export function Truncate({
  children,
  title,
  className,
  as: Comp = "span",
}: {
  children: ReactNode;
  title?: string;
  className?: string;
  as?: ElementType;
}) {
  const ref = useRef<HTMLElement>(null);
  const [cut, setCut] = useState(false);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const check = () => setCut(el.scrollWidth > el.clientWidth + 1);
    check();
    const ro = new ResizeObserver(check);
    ro.observe(el);
    return () => ro.disconnect();
  }, [children]);
  const inner = (
    <Comp ref={ref} className={cn("block min-w-0 truncate", className)}>
      {children}
    </Comp>
  );
  if (!cut) return inner;
  return (
    <Tooltip>
      <TooltipTrigger asChild>{inner}</TooltipTrigger>
      <TooltipContent>{title ?? (typeof children === "string" ? children : null)}</TooltipContent>
    </Tooltip>
  );
}
