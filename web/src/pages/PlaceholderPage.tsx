import { useTranslation } from "react-i18next";

export function PlaceholderPage({ titleKey }: { titleKey: string }) {
  const { t } = useTranslation();

  return (
    <div className="flex min-h-[calc(100vh-60px)] items-center justify-center">
      <div className="text-center">
        <h1 className="text-2xl font-bold text-[var(--ink-primary)]">{t(titleKey)}</h1>
        <p className="mt-2 text-[var(--ink-muted)]">{t("In development...")}</p>
      </div>
    </div>
  );
}
