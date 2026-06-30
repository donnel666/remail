export const INTERFACE_LANGUAGE_OPTIONS = [
  { code: "zh", label: "简体中文" },
  { code: "en", label: "English" },
  // { code: "fr", label: "Français" },
  // { code: "ru", label: "Русский" },
  // { code: "ja", label: "日本語" },
  // { code: "vi", label: "Tiếng Việt" },
] as const;

export type InterfaceLanguageCode = (typeof INTERFACE_LANGUAGE_OPTIONS)[number]["code"];

export function normalizeInterfaceLanguage(value?: string | null): InterfaceLanguageCode {
  if (!value) return "zh";

  const normalized = value.trim().replace(/_/g, "-").toLowerCase();
  if (normalized.startsWith("zh")) return "zh";

  return INTERFACE_LANGUAGE_OPTIONS.some((language) => language.code === normalized)
    ? (normalized as InterfaceLanguageCode)
    : "zh";
}
