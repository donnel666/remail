import { Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { CaptchaResponse } from "@/lib/iam-api";
import { cn } from "@/lib/utils";

interface CaptchaFieldProps {
  captcha: CaptchaResponse | null;
  loading: boolean;
  value: string;
  disabled?: boolean;
  onChange: (value: string) => void;
  onRefresh: () => void;
}

export function CaptchaField({
  captcha,
  loading,
  value,
  disabled,
  onChange,
  onRefresh,
}: CaptchaFieldProps) {
  const { t } = useTranslation();

  return (
    <div className="grid grid-cols-[1fr_112px] gap-2">
      <input
        type="text"
        inputMode="numeric"
        autoComplete="off"
        aria-label={t("Captcha")}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={t("Enter captcha")}
        className="input-antd min-w-0 w-full"
        disabled={disabled}
      />
      <button
        type="button"
        onClick={onRefresh}
        disabled={disabled || loading}
        title={t("Refresh captcha")}
        aria-label={t("Refresh captcha")}
        className={cn(
          "flex h-9 w-28 items-center justify-center overflow-hidden rounded-lg border border-[var(--divider)] bg-[var(--surface-sunken)] text-[var(--ink-muted)] transition-colors",
          "hover:border-[var(--brand-start)] hover:bg-[var(--surface-hover)] disabled:cursor-not-allowed disabled:opacity-60"
        )}
      >
        {loading ? (
          <Loader2 className="size-4 animate-spin text-[var(--ink-muted)]" />
        ) : captcha?.image ? (
          <img
            src={captcha.image}
            alt={t("Captcha image")}
            className="h-full w-full object-cover"
            draggable={false}
          />
        ) : (
          <span className="text-xs text-[var(--ink-muted)]">{t("Unavailable")}</span>
        )}
      </button>
    </div>
  );
}
