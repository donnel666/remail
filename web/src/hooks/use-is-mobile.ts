import { useEffect, useState } from "react";

export function useIsMobile(breakpoint = 767) {
  const getMatches = () =>
    typeof window === "undefined"
      ? false
      : window.matchMedia(`(max-width: ${breakpoint}px)`).matches;

  const [isMobile, setIsMobile] = useState(getMatches);

  useEffect(() => {
    const query = window.matchMedia(`(max-width: ${breakpoint}px)`);
    const onChange = () => setIsMobile(query.matches);

    onChange();
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, [breakpoint]);

  return isMobile;
}
