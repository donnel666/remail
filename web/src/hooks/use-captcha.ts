import { useCallback, useEffect, useRef, useState } from "react";
import { createCaptcha, type CaptchaResponse } from "@/lib/iam-api";

export function useCaptcha() {
  const requestRef = useRef(0);
  const [captcha, setCaptcha] = useState<CaptchaResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const refresh = useCallback(async () => {
    const requestId = requestRef.current + 1;
    requestRef.current = requestId;
    setLoading(true);
    setError(null);

    try {
      const next = await createCaptcha();
      if (requestRef.current === requestId) {
        setCaptcha(next);
      }
    } catch (nextError) {
      if (requestRef.current === requestId) {
        setCaptcha(null);
        setError(nextError);
      }
    } finally {
      if (requestRef.current === requestId) {
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return {
    captcha,
    loading,
    error,
    refresh,
  };
}
