// The Matrix list's windowing (pages/matrix.tsx): which rows to render for a scroll position.
// Pure, so ui/test/matrix-window.test.mjs can check the load budget without a browser.

export const ROW_H = 44; // px, every row
export const OVERSCAN = 8; // rows rendered above and below the view

/** The rows [first, last) to render when the list is scrolled to top (px) in a view height px tall. */
export function visibleRange(top: number, height: number, count: number): { first: number; last: number } {
  const first = Math.max(0, Math.min(count, Math.floor(top / ROW_H) - OVERSCAN));
  const last = Math.max(first, Math.min(count, Math.ceil((top + height) / ROW_H) + OVERSCAN));
  return { first, last };
}
