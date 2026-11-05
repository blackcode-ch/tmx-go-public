// A list from the daemon, whatever arrived: Go sends an empty slice as null, and an older or
// mocked daemon may answer an unknown route with an object. A page maps over the result, so
// anything that isn't an array is an empty list (agent details crashed on phones, 3 Nov).
export function asList<T>(v: unknown): T[] {
  return Array.isArray(v) ? (v as T[]) : [];
}
