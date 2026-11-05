import { useMemo } from "react";
import qrcode from "qrcode-generator";
import { cn } from "@/lib/utils";

/** A QR code drawn in the browser (qrcode-generator, pinned). Always dark on white, whatever
 * the theme, so every phone camera reads it. */
export function QRCode({ value, size = 232, className, label }: { value: string; size?: number; className?: string; label?: string }) {
  const { path, n } = useMemo(() => {
    const qr = qrcode(0, "M");
    qr.addData(value, "Byte");
    qr.make();
    const n = qr.getModuleCount();
    let d = "";
    for (let r = 0; r < n; r++) {
      for (let c = 0; c < n; c++) {
        if (!qr.isDark(r, c)) continue;
        // merge runs of dark modules on a row into one rectangle
        let w = 1;
        while (c + w < n && qr.isDark(r, c + w)) w++;
        d += `M${c} ${r}h${w}v1h-${w}z`;
        c += w - 1;
      }
    }
    return { path: d, n };
  }, [value]);
  const quiet = 3;
  return (
    <div className={cn("inline-flex rounded-xl bg-white p-2.5 shadow-sm ring-1 ring-black/10", className)}>
      <svg
        role="img"
        aria-label={label ?? "QR code"}
        width={size}
        height={size}
        viewBox={`${-quiet} ${-quiet} ${n + quiet * 2} ${n + quiet * 2}`}
        shapeRendering="crispEdges"
        className="block"
      >
        <rect x={-quiet} y={-quiet} width={n + quiet * 2} height={n + quiet * 2} fill="#fff" />
        <path d={path} fill="#0a0a0a" />
      </svg>
    </div>
  );
}
