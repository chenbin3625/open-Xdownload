// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import {
  applyThemeToDOM,
  initTheme,
  readThemePreference,
  setThemePreference,
  subscribeTheme,
  themeStorageKey,
} from "./theme";

describe("theme", () => {
  afterEach(() => {
    localStorage.removeItem(themeStorageKey);
    document.body.removeAttribute("theme-mode");
    document.documentElement.style.colorScheme = "";
  });

  it("switches body theme-mode and colorScheme for light and dark", () => {
    setThemePreference("dark");
    expect(readThemePreference()).toBe("dark");
    expect(localStorage.getItem(themeStorageKey)).toBe("dark");
    expect(document.body.getAttribute("theme-mode")).toBe("dark");
    expect(document.documentElement.style.colorScheme).toBe("dark");

    setThemePreference("light");
    expect(document.body.getAttribute("theme-mode")).toBeNull();
    expect(document.documentElement.style.colorScheme).toBe("light");
  });

  it("notifies subscribers and initializes from localStorage", () => {
    const seen: string[] = [];
    const unsubscribe = subscribeTheme((pref) => seen.push(pref));
    setThemePreference("dark");
    setThemePreference("system");
    unsubscribe();
    setThemePreference("light");
    expect(seen).toEqual(["dark", "system"]);

    localStorage.setItem(themeStorageKey, "dark");
    expect(initTheme()).toBe("dark");
    expect(applyThemeToDOM("dark")).toBe("dark");
  });
});
