import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { CheckCircle2 } from "lucide-react";
import { getRecharge } from "@/lib/wallet-api";

const RETURN_MESSAGE = "remail:epay-return";
const WAIT_SECONDS = 60;
const PENDING_STATUSES = new Set(["paying", "callback", "reconciled"]);

export default function PaymentReturn() {
  const [remaining, setRemaining] = useState(WAIT_SECONDS);
  const [credited, setCredited] = useState(false);
  const remainingRef = useRef(WAIT_SECONDS);
  const countdownRef = useRef<number | null>(null);
  const finishedRef = useRef(false);
  const rechargeNo = useMemo(
    () => new URLSearchParams(window.location.search).get("out_trade_no")?.trim(),
    []
  );

  const finish = useCallback(() => {
    if (finishedRef.current) return;
    finishedRef.current = true;
    if (countdownRef.current !== null) window.clearInterval(countdownRef.current);

    if (window.parent !== window) {
      window.parent.postMessage(RETURN_MESSAGE, window.location.origin);
      return;
    }
    window.location.replace("/wallet");
  }, []);

  const markCredited = useCallback(() => {
    if (finishedRef.current) return;
    if (countdownRef.current !== null) window.clearInterval(countdownRef.current);
    remainingRef.current = 5;
    setRemaining(5);
    setCredited(true);
  }, []);

  useEffect(() => {
    countdownRef.current = window.setInterval(() => {
      remainingRef.current -= 1;
      setRemaining(remainingRef.current);
      if (remainingRef.current <= 0) finish();
    }, 1000);

    return () => {
      if (countdownRef.current !== null) window.clearInterval(countdownRef.current);
    };
  }, [credited, finish]);

  useEffect(() => {
    if (!rechargeNo || credited) return;
    let cancelled = false;
    let checking = false;

    const checkRecharge = async () => {
      if (cancelled || checking) return;
      checking = true;
      try {
        const recharge = await getRecharge(rechargeNo);
        if (cancelled) return;
        if (recharge.status === "credited") {
          markCredited();
          return;
        }
        if (!PENDING_STATUSES.has(recharge.status)) finish();
      } catch {
        // The wallet page keeps checking even if this lightweight status check fails.
      } finally {
        checking = false;
      }
    };

    void checkRecharge();
    const timer = window.setInterval(() => void checkRecharge(), 5000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [credited, finish, markCredited, rechargeNo]);

  return (
    <main className="flex min-h-screen items-center justify-center bg-background px-5 py-8 text-foreground">
      <section
        aria-labelledby="payment-return-title"
        className="w-full max-w-md rounded-2xl border border-border bg-card px-6 py-10 text-center shadow-sm sm:px-10"
      >
        <span className="mx-auto flex size-16 items-center justify-center rounded-full bg-emerald-500/10 text-emerald-600 dark:text-emerald-400">
          <CheckCircle2 aria-hidden="true" className="size-9" strokeWidth={2} />
        </span>
        <h1 id="payment-return-title" className="mt-5 text-2xl font-semibold tracking-tight">
          {credited ? "充值已到账" : "支付完成"}
        </h1>
        <p className="mt-2 text-base leading-7 text-muted-foreground">
          {credited ? "余额已经更新，可以放心返回钱包" : "正在为你确认到账，余额会自动更新"}
        </p>
        <p className="mt-5 text-sm text-muted-foreground">
          {credited ? "支付窗口将在" : "到账后会自动返回，最长还需等待"}
          <strong className="mx-1 text-lg tabular-nums text-foreground">{remaining}</strong>
          {credited ? "秒后自动关闭" : "秒"}
        </p>

        <p aria-live="polite" className="sr-only" role="status">
          {credited
            ? "充值已到账，支付窗口将在 5 秒后自动关闭"
            : remaining <= 30
              ? "到账可能有延迟，返回钱包后系统仍会继续确认"
              : "正在确认到账"}
        </p>

        {!credited && remaining <= 30 ? (
          <p
            className="mt-5 rounded-xl bg-muted px-4 py-3 text-left text-sm leading-6 text-muted-foreground"
          >
            还没到账？别担心，支付渠道偶尔会有延迟。返回钱包后系统仍会继续确认；如果长时间未到账，请提交工单并附上充值订单号。
          </p>
        ) : null}

        <button
          className="mt-6 h-11 min-w-40 cursor-pointer rounded-lg bg-primary px-6 text-sm font-semibold text-primary-foreground transition-colors duration-200 hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 focus-visible:ring-offset-background"
          onClick={finish}
          type="button"
        >
          立即返回钱包
        </button>
      </section>
    </main>
  );
}
