// API calls for People, Clients, Promises and Reminders (internal/api/people.go).
import { useStore } from "./store";
import type { Person, Promise_, PromiseStatus, Reminder } from "./people-format";

function t() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}

const enc = encodeURIComponent;

export interface PersonEdit {
  key: string;
  name?: string;
  role?: string;
  lang?: string;
  kind?: string;
  preferred?: string;
  notes?: string;
  /** "" removes a channel */
  channels?: { whatsapp?: string; email?: string; github?: string };
}

export const peopleApi = {
  list: () => t().request<Person[]>("GET", "/api/team"),
  put: (p: PersonEdit) => t().request<Person>("POST", "/api/team", p),
  remove: (key: string) => t().request<void>("DELETE", `/api/team/${enc(key)}`),
  promises: (person = "") => t().request<Promise_[]>("GET", `/api/promises${person ? "?person=" + enc(person) : ""}`),
  addPromise: (b: { person_key: string; title: string; due?: string; owner?: string; text?: string }) =>
    t().request<Promise_>("POST", "/api/promises", b),
  setPromise: (id: string, b: { status?: PromiseStatus; due?: string; title?: string }) =>
    t().request<Promise_>("POST", `/api/promises/${enc(id)}`, b),
  reminders: (state = "pending") => t().request<Reminder[]>("GET", `/api/reminders?state=${enc(state)}`),
  addReminder: (b: { at: number; text: string; target?: string }) => t().request<Reminder>("POST", "/api/reminders", b),
  cancelReminder: (id: string) => t().request<void>("DELETE", `/api/reminders/${enc(id)}`),
};
