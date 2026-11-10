import { create } from "zustand";

interface UIState {
  importOpen: boolean;
  setImportOpen: (open: boolean) => void;
  compact: boolean;
  setCompact: (compact: boolean) => void;
}
export const useUI = create<UIState>((set) => ({
  importOpen: false,
  setImportOpen: (importOpen) => set({ importOpen }),
  compact: false,
  setCompact: (compact) => set({ compact }),
}));
