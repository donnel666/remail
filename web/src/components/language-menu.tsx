import { Languages } from "lucide-react";
import { useTranslation } from "react-i18next";
import { HeaderActionButton } from "@/components/header-action-button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  INTERFACE_LANGUAGE_OPTIONS,
  normalizeInterfaceLanguage,
} from "@/i18n/languages";
import { cn } from "@/lib/utils";

export function LanguageMenu() {
  const { i18n, t } = useTranslation();
  const currentLanguage = normalizeInterfaceLanguage(i18n.language);

  return (
    <DropdownMenu modal={false} openOnHover>
      <DropdownMenuTrigger
        render={<HeaderActionButton aria-label={t("Change language")} />}
      >
        <Languages />
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        className="w-[120px] min-w-[120px] rounded-lg border-0 bg-popover p-0 shadow-[0_0_1px_rgba(0,0,0,0.3),0_4px_14px_rgba(0,0,0,0.1)]"
      >
        <div className="py-1" role="menu" aria-label={t("Change language")}>
          {INTERFACE_LANGUAGE_OPTIONS.map((language) => (
            <DropdownMenuItem
              key={language.code}
              role="menuitem"
              onClick={() => void i18n.changeLanguage(language.code)}
              className={cn(
                "h-8 rounded-none px-3 py-1.5 text-sm text-foreground hover:bg-surface-sunken",
                currentLanguage === language.code && "bg-brand-subtle font-semibold"
              )}
            >
              {language.label}
            </DropdownMenuItem>
          ))}
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
