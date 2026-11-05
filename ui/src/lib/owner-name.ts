// The owner's display name. The private build names the owner in owner_private.ts; public-scrub.sh
// deletes every *_private.ts, and the optional glob import below is then simply empty, so a public
// build says "the owner" (scrub-neutral, tmx-lead 4 Nov).
const named = import.meta.glob<string>("./owner_private.ts", { eager: true, import: "OWNER_NAME" });

export const ownerName: string = Object.values(named)[0] ?? "the owner";
