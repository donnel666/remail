import { useEffect } from "react";
import { Check, Moon, Sun } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { useTheme } from "@/context/theme-provider";
import { HeaderActionButton } from "@/components/header-action-button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

const THEME_ITEMS = [
  { value: "light", labelKey: "Light" },
  { value: "dark", labelKey: "Dark" },
  { value: "system", labelKey: "System" },
] as const;

export function ThemeSwitch() {
  const { t } = useTranslation();
  const { theme, resolvedTheme, setTheme } = useTheme();

  useEffect(() => {
    const themeColor = resolvedTheme === "dark" ? "#171717" : "#fff";
    const metaThemeColor = document.querySelector("meta[name='theme-color']");
    if (metaThemeColor) metaThemeColor.setAttribute("content", themeColor);
  }, [resolvedTheme]);

  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger
        render={
          <HeaderActionButton aria-label={t("Toggle theme")} className="relative" />
        }
      >
        <Sun className="size-[18px] scale-100 rotate-0 transition-all dark:scale-0 dark:-rotate-90" />
        <Moon className="absolute size-[18px] scale-0 rotate-90 transition-all dark:scale-100 dark:rotate-0" />
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        className="w-[120px] rounded-md border-0 bg-popover p-0 shadow-[0_0_1px_rgba(0,0,0,0.3),0_4px_14px_rgba(0,0,0,0.1)]"
      >
        <div className="py-1" role="menu" aria-label={t("Toggle theme")}>
          {THEME_ITEMS.map((item) => (
            <DropdownMenuItem
              key={item.value}
              role="menuitem"
              onClick={() => setTheme(item.value)}
              className={cn(
                "h-8 rounded-none px-3 py-1.5 text-sm text-foreground hover:bg-surface-sunken",
                theme === item.value && "bg-brand-subtle font-semibold"
              )}
            >
              {t(item.labelKey)}
              <Check
                size={14}
                className={cn("ml-auto", theme !== item.value && "hidden")}
              />
            </DropdownMenuItem>
          ))}
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
