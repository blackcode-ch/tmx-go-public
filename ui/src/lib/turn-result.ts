import type { TimelineEntry } from "./types";

export function turnResult(e: TimelineEntry): { failed: boolean; label: string } {
  const subtype = e.meta?.subtype;
  switch (subtype) {
    case "interrupted":
      return { failed: false, label: e.meta?.owner_interrupt === true ? "Interrupted" : "Ended" };
    case "aborted":
    case "superseded":
      return { failed: false, label: "Ended" };
    default: {
      if (e.is_error && e.text?.trim().replace(/^\[|\]$/g, "").toLowerCase() === "request interrupted by user") {
        return { failed: false, label: "Ended" };
      }
      const failed = !!e.is_error || (typeof subtype === "string" && subtype !== "success");
      return {
        failed,
        label: failed ? (typeof subtype === "string" && subtype !== "success" ? subtype.replace(/_/g, " ") : "Ended with an error") : "Done",
      };
    }
  }
}
