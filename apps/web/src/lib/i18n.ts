import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import zhCN from "../locales/zh-CN.json";
import en from "../locales/en.json";

export { useTranslation } from "react-i18next";
export type LanguagePreference = "auto" | "zh-CN" | "en";
export const languageStorageKey = "open-xdownload-language";
let sessionPreference: LanguagePreference | undefined;

export function browserLanguage(languages: readonly string[] = []): "zh-CN" | "en" {
  for (const language of languages) {
    if (/^zh(?:-|$)/i.test(language)) return "zh-CN";
    if (/^en(?:-|$)/i.test(language)) return "en";
  }
  return "en";
}

export function readLanguagePreference(): LanguagePreference {
  if (sessionPreference !== undefined) return sessionPreference;
  try {
    const stored = localStorage.getItem(languageStorageKey);
    if (stored === "zh-CN" || stored === "en") return stored;
  } catch {
    // Language switching still works when browser storage is unavailable.
  }
  return "auto";
}

function resolveLanguage(preference: LanguagePreference) {
  return preference === "auto"
    ? browserLanguage(typeof navigator === "undefined" ? [] : navigator.languages?.length ? navigator.languages : [navigator.language])
    : preference;
}

void i18n.use(initReactI18next).init({
  resources: { "zh-CN": { translation: zhCN }, en: { translation: en } },
  lng: resolveLanguage(readLanguagePreference()),
  fallbackLng: "zh-CN",
  supportedLngs: ["zh-CN", "en"],
  keySeparator: false,
  nsSeparator: false,
  interpolation: { escapeValue: false },
  react: { useSuspense: false },
  initAsync: false,
});

function updateDocumentLanguage(language: string) {
  if (typeof document !== "undefined") document.documentElement.lang = language;
}
i18n.on("languageChanged", updateDocumentLanguage);
updateDocumentLanguage(i18n.language);

export async function setLanguagePreference(preference: LanguagePreference) {
  sessionPreference = preference;
  try {
    localStorage.setItem(languageStorageKey, preference);
  } catch {
    // Keep the selected language for this session even if it cannot be saved.
  }
  await i18n.changeLanguage(resolveLanguage(preference));
}

export const t = i18n.t.bind(i18n);
export default i18n;
