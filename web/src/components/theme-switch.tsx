import { useEffect } from "react";
import { Monitor, Moon, Sun } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { useTheme } from "@/context/theme-provider";
import { HeaderActionButton } from "@/components/header-action-button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

const THEME_ITEMS = [
  {
    value: "light",
    labelKey: "Light mode",
    descriptionKey: "Always use light theme",
    icon: Sun,
  },
  {
    value: "dark",
    labelKey: "Dark mode",
    descriptionKey: "Always use dark theme",
    icon: Moon,
  },
  {
    value: "system",
    labelKey: "Auto mode",
    descriptionKey: "Follow system theme",
    icon: Monitor,
  },
] as const;

export function ThemeSwitch() {
  const { t } = useTranslation();
  const { theme, resolvedTheme, setTheme } = useTheme();
  const currentTheme = THEME_ITEMS.find((item) => item.value === theme) ?? THEME_ITEMS[2];
  const CurrentIcon = currentTheme.icon;

  useEffect(() => {
    const themeColor = resolvedTheme === "dark" ? "#171717" : "#fff";
    const metaThemeColor = document.querySelector("meta[name='theme-color']");
    if (metaThemeColor) metaThemeColor.setAttribute("content", themeColor);
  }, [resolvedTheme]);

  return (
    <DropdownMenu modal={false} openOnHover>
      <DropdownMenuTrigger
        render={<HeaderActionButton aria-label={t("Toggle theme")} />}
      >
        <CurrentIcon className="size-[18px]" />
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        className="w-[154px] rounded-lg border-0 bg-popover p-0 shadow-[0_0_1px_rgba(0,0,0,0.3),0_4px_14px_rgba(0,0,0,0.1)]"
      >
        <div className="py-1" role="menu" aria-label={t("Toggle theme")}>
          {THEME_ITEMS.map((item) => (
            <DropdownMenuItem
              key={item.value}
              role="menuitem"
              onClick={() => setTheme(item.value)}
              className={cn(
                "min-h-[52px] gap-2 rounded-none px-4 py-2 text-sm text-foreground hover:bg-surface-sunken",
                theme === item.value && "bg-brand-subtle font-semibold"
              )}
            >
              <item.icon className="size-[18px] shrink-0 text-foreground/80" />
              <span className="flex min-w-0 flex-col">
                <span>{t(item.labelKey)}</span>
                <span className="text-xs font-normal text-muted-foreground">
                  {t(item.descriptionKey)}
                </span>
              </span>
            </DropdownMenuItem>
          ))}
          {theme === "system" ? (
            <>
              <DropdownMenuSeparator className="mx-0 my-1 bg-border" />
              <div className="px-3 py-2 text-xs text-muted-foreground">
                {t("Currently follows system")}:{" "}
                {resolvedTheme === "dark" ? t("Dark short") : t("Light short")}
              </div>
            </>
          ) : null}
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
