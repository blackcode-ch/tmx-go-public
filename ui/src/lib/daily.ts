// Home as "Daily" (#235 decision 2; the Python dashboard's phone home): a greeting by the time of
// day and one plain line about what moves and what waits. Pure, so node can test it.
export function greeting(hour: number): string {
  if (hour >= 5 && hour < 12) return "Good morning.";
  if (hour >= 12 && hour < 18) return "Good afternoon.";
  return "Good evening.";
}

const words = ["No", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine", "Ten"];
const num = (n: number) => (n <= 10 ? words[n] : String(n));

/** "Four things move, one waits for you." / "All quiet: nothing waits for you." */
export function dailyLine(working: number, waiting: number): string {
  const moves = working === 0 ? "" : `${num(working)} ${working === 1 ? "thing moves" : "things move"}`;
  const waits = waiting === 0 ? "nothing waits for you" : `${num(waiting).toLowerCase()} ${waiting === 1 ? "waits" : "wait"} for you`;
  if (!moves) return waiting === 0 ? "All quiet: nothing waits for you." : `${num(waiting)} ${waiting === 1 ? "waits" : "wait"} for you.`;
  return `${moves}, ${waits}.`;
}
