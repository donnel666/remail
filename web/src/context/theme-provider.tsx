import { createContext, useCallback, useContext, useEffect, useState } from "react";

type Theme = "dark" | "light" | "system";
type ResolvedTheme = Exclude<Theme, "system">;

interface ThemeProviderProps {
  children: React.ReactNode;
  defaultTheme?: Theme;
  storageKey?: string;
}

interface ThemeProviderState {
  defaultTheme: Theme;
  resolvedTheme: ResolvedTheme;
  theme: Theme;
  setTheme: (theme: Theme) => void;
  resetTheme: () => void;
}

const initialState: ThemeProviderState = {
  defaultTheme: "system",
  resolvedTheme: "light",
  theme: "system",
  setTheme: () => null,
  resetTheme: () => null,
};

const ThemeContext = createContext<ThemeProviderState>(initialState);

function getSystemTheme(): ResolvedTheme {
  if (typeof window === "undefined") return "light";
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function resolveTheme(theme: Theme): ResolvedTheme {
  return theme === "system" ? getSystemTheme() : theme;
}

function getStoredTheme(storageKey: string, fallback: Theme): Theme {
  try {
    const stored = localStorage.getItem(storageKey) as Theme | null;
    return stored ? stored : fallback;
  } catch {
    return fallback;
  }
}

export function ThemeProvider({
  children,
  defaultTheme = "system",
  storageKey = "remail-ui-theme",
}: ThemeProviderProps) {
  const [theme, _setTheme] = useState<Theme>(() => getStoredTheme(storageKey, defaultTheme));
  const [resolvedTheme, setResolvedTheme] = useState<ResolvedTheme>(() =>
    resolveTheme(getStoredTheme(storageKey, defaultTheme))
  );

  useEffect(() => {
    const root = window.document.documentElement;
    const mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");

    const applyTheme = () => {
      const nextResolvedTheme = theme === "system" ? getSystemTheme() : theme;
      root.classList.remove("light", "dark");
      root.classList.add(nextResolvedTheme);
      setResolvedTheme(nextResolvedTheme);
    };

    applyTheme();
    mediaQuery.addEventListener("change", applyTheme);
    return () => mediaQuery.removeEventListener("change", applyTheme);
  }, [theme]);

  const setTheme = useCallback(
    (theme: Theme) => {
      try {
        localStorage.setItem(storageKey, theme);
      } catch {}
      _setTheme(theme);
    },
    [storageKey]
  );

  const resetTheme = useCallback(() => {
    try {
      localStorage.removeItem(storageKey);
    } catch {}
    _setTheme(defaultTheme);
  }, [defaultTheme, storageKey]);

  return (
    <ThemeContext.Provider value={{ defaultTheme, resolvedTheme, theme, setTheme, resetTheme }}>
      {children}
    </ThemeContext.Provider>
  );
}

export function useTheme() {
  const context = useContext(ThemeContext);
  if (context === undefined) {
    throw new Error("useTheme must be used within a ThemeProvider");
  }
  return context;
}
