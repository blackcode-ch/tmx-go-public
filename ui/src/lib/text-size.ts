// Text size (the owner, 3 Nov: "everything is too tiny… I have bad eyes"): the whole UI is sized in
// rem, so one root font-size scales every page. Normal is 125% of the old size (the new default,
// also in index.css for the first paint); Large and Larger go further. Saved per device.
export type TextSize = "normal" | "large" | "larger";

export const TEXT_SIZES: { id: TextSize; label: string; percent: number }[] = [
  { id: "normal", label: "Normal", percent: 125 },
  { id: "large", label: "Large", percent: 140 },
  { id: "larger", label: "Larger", percent: 160 },
];

const KEY = "tmx.textSize";

export function textSizePercent(s: string | null | undefined): number {
  return (TEXT_SIZES.find((t) => t.id === s) ?? TEXT_SIZES[0]).percent;
}

export function savedTextSize(): TextSize {
  try {
    const s = localStorage.getItem(KEY);
    return TEXT_SIZES.some((t) => t.id === s) ? (s as TextSize) : "normal";
  } catch {
    return "normal";
  }
}

/** Applies a text size to the page and saves it on this device. */
export function applyTextSize(s: TextSize, save = true) {
  const root = document.documentElement;
  root.style.fontSize = `${textSizePercent(s)}%`;
  root.dataset.textSize = s;
  if (save) {
    try {
      localStorage.setItem(KEY, s);
    } catch {
      // private mode: the size still applies for this visit
    }
  }
  window.dispatchEvent(new Event("tmx-text-size"));
}

/** How many CSS pixels one rem is now, over 16: for the few sizes JavaScript lays out in px
 * (the canvas's lanes, the Matrix list's rows, the terminal's font). */
export function remScale(): number {
  if (typeof document === "undefined") return 1;
  const px = parseFloat(getComputedStyle(document.documentElement).fontSize);
  return px > 0 ? px / 16 : 1;
}
