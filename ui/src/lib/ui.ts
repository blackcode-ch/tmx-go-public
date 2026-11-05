// Ephemeral UI state: dialogs, the mobile nav sheet, focus requests.
import { create } from "zustand";

interface UIState {
	showAllAgents: boolean;
  palette: boolean;
  shortcuts: boolean;
  newProject: boolean;
  mobileNav: boolean;
  /** bumped to ask the current page to focus its composer */
  focusComposer: number;
  /** bumped to ask the overview to focus the head composer */
  focusHead: number;
}

export const useUI = create<UIState>(() => ({
	showAllAgents: false,
  palette: false,
  shortcuts: false,
  newProject: false,
  mobileNav: false,
  focusComposer: 0,
  focusHead: 0,
}));

export const ui = {
  set: useUI.setState,
  openPalette: (v = true) => useUI.setState({ palette: v }),
  openShortcuts: (v = true) => useUI.setState({ shortcuts: v }),
  openNewProject: (v = true) => useUI.setState({ newProject: v, palette: false, mobileNav: false }),
  openMobileNav: (v = true) => useUI.setState({ mobileNav: v }),
  focusComposer: () => useUI.setState((s) => ({ focusComposer: s.focusComposer + 1 })),
  focusHead: () => useUI.setState((s) => ({ focusHead: s.focusHead + 1 })),
};

export function isMacLike(): boolean {
  return /mac|iphone|ipad/i.test(navigator.platform || navigator.userAgent);
}

export const MOD = isMacLike() ? "⌘" : "Ctrl";
