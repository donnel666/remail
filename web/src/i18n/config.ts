import i18n from "i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import { initReactI18next } from "react-i18next";
import { normalizeInterfaceLanguage } from "./languages";
import en from "./locales/en.json";
import zh from "./locales/zh.json";

export const resources = {
  en: { translation: en },
  zh: { translation: zh },
} as const;

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources,
    fallbackLng: "zh",
    supportedLngs: ["zh", "en"],
    load: "languageOnly",
    nsSeparator: false,
    debug: import.meta.env.DEV,
    interpolation: {
      escapeValue: false,
    },
    detection: {
      order: ["localStorage", "navigator"],
      caches: ["localStorage"],
      convertDetectedLanguage: normalizeInterfaceLanguage,
    },
  });

function syncHtmlLanguage(language: string) {
  if (typeof document === "undefined") return;
  document.documentElement.lang = normalizeInterfaceLanguage(language);
}

i18n.on("languageChanged", syncHtmlLanguage);
syncHtmlLanguage(i18n.resolvedLanguage ?? i18n.language);

export default i18n;
