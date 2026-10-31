// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import i18n, { browserLanguage, languageStorageKey, readLanguagePreference, setLanguagePreference } from "./i18n";
import { formatDateTime, formatIntervalMinutes } from "../components/common/CommonUI";
import { getJobProgressDisplay, parseAggregatedErrors } from "./jobErrors";
import zh from "../locales/zh-CN.json";
import en from "../locales/en.json";
import type { Job } from "./api";

afterEach(async () => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  await setLanguagePreference("auto");
  localStorage.removeItem(languageStorageKey);
});

it.each([
  [["zh-TW", "en-US"], "zh-CN"],
  [["en-GB", "zh-CN"], "en"],
  [["fr-FR", "zh-HK"], "zh-CN"],
  [["de-DE"], "en"],
  [[], "en"],
])("resolves supported browser languages %j to %s", (languages, expected) => {
  expect(browserLanguage(languages)).toBe(expected);
});

it("persists an explicit language and resolves browser default again", async () => {
  vi.stubGlobal("navigator", { languages: ["en-GB"], language: "en-GB" });
  await setLanguagePreference("zh-CN");
  expect(readLanguagePreference()).toBe("zh-CN");
  expect(document.documentElement.lang).toBe("zh-CN");
  await setLanguagePreference("auto");
  expect(readLanguagePreference()).toBe("auto");
  expect(document.documentElement.lang).toBe("en");
});

it("ignores unsupported stored values and still switches when storage is blocked", async () => {
  localStorage.setItem(languageStorageKey, "unsupported");
  expect(readLanguagePreference()).toBe("auto");
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("blocked"); });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("blocked"); });
  expect(readLanguagePreference()).toBe("auto");
  await setLanguagePreference("en");
  expect(document.documentElement.lang).toBe("en");
  expect(readLanguagePreference()).toBe("en");
});

it("keeps language keys and interpolation variables in sync", () => {
  expect(Object.keys(en).sort()).toEqual(Object.keys(zh).sort());
  for (const key of Object.keys(zh) as (keyof typeof zh)[]) {
    expect(en[key].trim().length, key).toBeGreaterThan(0);
    expect(en[key].match(/{{\w+}}/g)?.sort(), key).toEqual(zh[key].match(/{{\w+}}/g)?.sort());
    expect(en[key], key).not.toMatch(/[\u3400-\u9fff]/);
  }
});

it("formats dates and intervals in the active language", async () => {
  const value = "2026-10-31T09:25:00Z";
  for (const language of ["zh-CN", "en"] as const) {
    await i18n.changeLanguage(language);
    expect(formatDateTime(value)).toBe(new Intl.DateTimeFormat(language, {
      year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit",
    }).format(new Date(value)));
  }
  expect(formatIntervalMinutes(60)).toBe("Every 1 hour");
  expect(formatIntervalMinutes(120)).toBe("Every 2 hours");
  expect(formatDateTime("invalid")).toBe("invalid");
});

it("translates frontend error summaries while preserving backend diagnostics", async () => {
  const backend = "读取 @alice 的媒体时间线失败: 后端原始错误";
  await i18n.changeLanguage("en");
  expect(parseAggregatedErrors(backend)[0].reason).toBe("后端原始错误");
  expect(parseAggregatedErrors("另有 3 种其他异常")[0].reason).toBe("Other errors");
  expect(getJobProgressDisplay({ status: "completed", message: "后端原文" } as Job).displayMsg).toBe("后端原文");
});
