// A report's key moments (the Python recorder's review_moments, ported; the owner, 3 Nov: he points and highlights
// on screen, and the reports must show "the screenshots the OLD reports had"): the stills taken because the screen
// changed (frame.key: a new page, a highlight, a drawing), the still just after each moment the transcript points
// at something ("look at this", "here", in his languages), and the first still. At most MAX_MOMENTS, none within
// MOMENT_GAP_MS of another; a spoken moment wins over a change, a change over the start. Pure: no imports.

export const MAX_MOMENTS = 24;
export const MOMENT_GAP_MS = 8000;
export const MOMENT_NEAR_MS = 20_000;

export const POINTING =
  /(^|[^\p{L}])(look|see|here|this one|that one|notice|highlight\p{L}*|watch|right there|over there|schau|siehst|hier|regarde[zs]?|voil[aà]|ici|guarda|ecco|qui|mira|aqu[ií])(?![\p{L}])/iu;

export interface MomentFrame {
  ms: number;
  key?: boolean;
}

/** A line of the transcript at ms of recorded time (the whole recording's, not its part's). */
export interface MomentLine {
  ms: number;
  text: string;
}

export interface Moment {
  ms: number;
  why: "start" | "screen changed" | "said";
  /** what was said (why "said") */
  said?: string;
}

export function keyMoments(frames: MomentFrame[], lines: MomentLine[] = []): Moment[] {
  if (!frames.length) return [];
  const shots = [...frames].sort((a, b) => a.ms - b.ms);
  type Pick = { rank: number; ms: number; why: Moment["why"]; said?: string };
  const picks: Pick[] = [{ rank: 2, ms: shots[0].ms, why: "start" }];
  for (const f of shots) if (f.key) picks.push({ rank: 1, ms: f.ms, why: "screen changed" });
  for (const l of lines) {
    if (!POINTING.test(l.text)) continue;
    // the still just after the words (he says "look", then it shows), else the nearest
    const after = shots.filter((f) => f.ms >= l.ms - 2000);
    const pool = after.length ? after : shots;
    let best = pool[0];
    for (const f of pool) if (Math.abs(f.ms - l.ms) < Math.abs(best.ms - l.ms)) best = f;
    if (Math.abs(best.ms - l.ms) <= MOMENT_NEAR_MS)
      picks.push({
        rank: 0,
        ms: best.ms,
        why: "said",
        said: l.text.trim().slice(0, 160),
      });
  }
  picks.sort((a, b) => a.rank - b.rank || a.ms - b.ms);
  const out: Moment[] = [];
  for (const p of picks) {
    if (out.length >= MAX_MOMENTS) break;
    if (out.every((o) => Math.abs(o.ms - p.ms) >= MOMENT_GAP_MS)) out.push(p.said ? { ms: p.ms, why: p.why, said: p.said } : { ms: p.ms, why: p.why });
  }
  return out.sort((a, b) => a.ms - b.ms);
}

/** The segment and the time in it of ms of recorded time. */
export function place(segments: { n: number; duration_ms: number }[], ms: number): { n: number; ms: number } {
  let t = 0;
  for (let i = 0; i < segments.length; i++) {
    const g = segments[i];
    if (ms < t + g.duration_ms || i === segments.length - 1) return { n: g.n, ms: Math.max(0, ms - t) };
    t += g.duration_ms;
  }
  return { n: 1, ms };
}

/** Where each segment starts in the whole recording. */
export function segmentStarts(segments: { n: number; duration_ms: number }[]): Map<number, number> {
  const m = new Map<number, number>();
  let t = 0;
  for (const g of segments) {
    m.set(g.n, t);
    t += g.duration_ms;
  }
  return m;
}

/** The index of the transcript line being heard at ms of its part (-1: none): the last one begun, until 1.5 s after it ends. Lines are in time order. */
export function lineAt(lines: { from_ms: number; to_ms: number }[], ms: number): number {
  let i = -1;
  while (i + 1 < lines.length && lines[i + 1].from_ms <= ms) i++;
  return i >= 0 && ms < Math.max(lines[i].to_ms, lines[i].from_ms) + 1500 ? i : -1;
}
