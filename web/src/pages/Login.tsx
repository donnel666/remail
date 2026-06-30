import { useTranslation } from "react-i18next";

export default function Login() {
  const { t } = useTranslation();

  return (
    <div className="flex items-center justify-center min-h-screen bg-[var(--canvas)]">
      <div className="w-full max-w-sm rounded-xl border border-[var(--divider)] bg-[var(--surface)] p-8 shadow-sm">
        <div className="flex flex-col items-center gap-2 mb-8">
          <img src="/logo.png" alt="Remail" className="h-12 w-12" />
          <h1 className="text-xl font-bold text-[var(--ink-primary)]">Remail</h1>
          <p className="text-sm text-[var(--ink-muted)]">{t("Log in to your account")}</p>
        </div>
        <div className="space-y-4">
          <input type="text" placeholder={t("Username")} className="input-antd w-full" />
          <input type="password" placeholder={t("Password")} className="input-antd w-full" />
          <button className="w-full h-10 rounded-lg bg-gradient-to-br from-[var(--brand-start)] to-[var(--brand-end)] text-[14px] font-semibold text-white shadow-sm hover:shadow-md transition-all">
            {t("Login")}
          </button>
        </div>
      </div>
    </div>
  );
}
