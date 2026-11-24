export type ThemePreference = "system" | "light" | "dark";

export const themeStorageKey = "open-xdownload-theme";
const listeners = new Set<(pref: ThemePreference) => void>();

let currentPreference: ThemePreference = "system";
let initialized = false;
let mediaQuery: MediaQueryList | null = null;

function isValidTheme(value: string | null): value is ThemePreference {
  return value === "system" || value === "light" || value === "dark";
}

function readStoredPreference(): ThemePreference {
  try {
    const stored = localStorage.getItem(themeStorageKey);
    if (isValidTheme(stored)) return stored;
  } catch {
    // Private browsing or blocked storage
  }
  return "system";
}

export function resolveEffectiveTheme(pref: ThemePreference): "light" | "dark" {
  if (pref === "light" || pref === "dark") return pref;
  if (typeof window !== "undefined" && typeof window.matchMedia === "function") {
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  return "light";
}

export function applyThemeToDOM(pref: ThemePreference): "light" | "dark" {
  const effective = resolveEffectiveTheme(pref);
  if (typeof document !== "undefined") {
    if (effective === "dark") {
      document.body?.setAttribute("theme-mode", "dark");
    } else {
      document.body?.removeAttribute("theme-mode");
    }
    if (document.documentElement) {
      document.documentElement.style.colorScheme = effective;
    }
  }
  return effective;
}

export function initTheme(): ThemePreference {
  currentPreference = readStoredPreference();
  applyThemeToDOM(currentPreference);
  if (!initialized && typeof window !== "undefined" && typeof window.matchMedia === "function") {
    initialized = true;
    mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
    const handleChange = () => {
      if (currentPreference === "system") {
        applyThemeToDOM("system");
        listeners.forEach((fn) => fn("system"));
      }
    };
    if (typeof mediaQuery.addEventListener === "function") {
      mediaQuery.addEventListener("change", handleChange);
    } else if (typeof mediaQuery.addListener === "function") {
      mediaQuery.addListener(handleChange);
    }
  }
  return currentPreference;
}

export function readThemePreference(): ThemePreference {
  if (!initialized) {
    currentPreference = readStoredPreference();
  }
  return currentPreference;
}

export function setThemePreference(pref: ThemePreference): void {
  currentPreference = pref;
  try {
    localStorage.setItem(themeStorageKey, pref);
  } catch {
    // Ignore storage errors
  }
  applyThemeToDOM(pref);
  listeners.forEach((fn) => fn(pref));
}

export function subscribeTheme(listener: (pref: ThemePreference) => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
