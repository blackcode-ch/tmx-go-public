// The canonical Living cell mark, shared with the Python dashboard.
import { cn } from "@/lib/utils";

export function BrandMark({ className }: { className?: string }) {
  return <img src="/living-cell.svg" alt="Tiamatrix" className={cn("inline-block size-6 shrink-0", className)} />;
}
