// Tiamatrix's mark: the Pocket dragon (Tiamat), the exact header mark of the Python dashboard's
// Tiamatrix design (terminal-matrix tmx/design/tiamatrix-green/header-mark.svg, copied from git
// into public/brand). Drawn as a mask so it takes the theme's green; the owner, 3 Nov: the grid
// logo is out, the snake stays, and bigger.
import type { CSSProperties } from "react";
import { cn } from "@/lib/utils";

const mask: CSSProperties = {
  maskImage: "url(/brand/header-mark.svg)",
  WebkitMaskImage: "url(/brand/header-mark.svg)",
  maskSize: "contain",
  WebkitMaskSize: "contain",
  maskRepeat: "no-repeat",
  WebkitMaskRepeat: "no-repeat",
  maskPosition: "center",
  WebkitMaskPosition: "center",
};

export function BrandMark({ className }: { className?: string }) {
  return <span role="img" aria-label="Tiamatrix" className={cn("inline-block size-6 shrink-0 bg-[#356348] dark:bg-[#b7d3a3]", className)} style={mask} />;
}
