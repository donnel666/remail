import { useCallback, useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { createRoot } from "react-dom/client";

import { TurnstileField } from "./TurnstileField";

// Turnstile in managed mode solves itself for most real browsers, so popping a
// dialog immediately would make it flash open and shut on every guarded action.
// The widget is therefore always mounted and laid out; only its wrapper becomes
// a visible dialog, and only if the challenge is still unsolved after this long
// — i.e. only when it actually needs a human. The widget must never be hidden
// with display:none or unmounted, or Cloudflare cannot render it.
const REVEAL_DELAY_MS = 600;
const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), iframe, [tabindex]:not([tabindex="-1"]):not([data-turnstile-sentinel])';

let cancelActiveTurnstile: (() => void) | undefined;

interface TurnstileGateProps {
  action: string;
  onSettle: (token: string | null) => void;
}

function TurnstileGate({ action, onSettle }: TurnstileGateProps) {
  const { t } = useTranslation();
  const [visible, setVisible] = useState(false);
  const dialogRef = useRef<HTMLDivElement>(null);
  const previousFocusRef = useRef<HTMLElement | null>(null);
  const settleRef = useRef(onSettle);
  const visibleRef = useRef(visible);
  const titleId = useId();
  const descriptionId = useId();
  settleRef.current = onSettle;
  visibleRef.current = visible;

  const focusableElements = useCallback(
    () =>
      Array.from(
        dialogRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR) ?? []
      ),
    []
  );

  useEffect(() => {
    const timer = globalThis.setTimeout(() => {
      previousFocusRef.current =
        document.activeElement instanceof HTMLElement
          ? document.activeElement
          : null;
      setVisible(true);
    }, REVEAL_DELAY_MS);
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        settleRef.current(null);
        return;
      }
      if (event.key !== "Tab" || !visibleRef.current) return;

      const elements = focusableElements();
      const first = elements[0];
      const last = elements[elements.length - 1];
      if (!first || !last) {
        event.preventDefault();
        dialogRef.current?.focus();
        return;
      }
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => {
      globalThis.clearTimeout(timer);
      document.removeEventListener("keydown", onKeyDown);
      if (previousFocusRef.current?.isConnected) {
        previousFocusRef.current.focus();
      }
    };
  }, [focusableElements]);

  useEffect(() => {
    if (!visible) return;
    const elements = focusableElements();
    (elements[0] ?? dialogRef.current)?.focus();
  }, [focusableElements, visible]);

  const handleToken = useCallback((token: string) => {
    // TurnstileField clears the token on mount and on expiry; only a non-empty
    // value means the challenge passed.
    if (token) settleRef.current(token);
  }, []);

  return (
    <div
      aria-hidden={visible ? undefined : true}
      className={
        visible
          ? "fixed inset-0 z-[1100] flex items-center justify-center bg-black/40"
          : "pointer-events-none fixed left-0 top-0 -z-10 w-80 opacity-0"
      }
      inert={visible ? undefined : true}
      onClick={visible ? () => settleRef.current(null) : undefined}
    >
      <div
        aria-describedby={visible ? descriptionId : undefined}
        aria-labelledby={visible ? titleId : undefined}
        aria-modal={visible ? true : undefined}
        className={
          visible
            ? "w-80 rounded-lg bg-[var(--surface)] p-6 shadow-xl"
            : "w-full"
        }
        ref={dialogRef}
        role={visible ? "dialog" : undefined}
        tabIndex={visible ? -1 : undefined}
        onClick={(event) => event.stopPropagation()}
      >
        {visible ? (
          <>
            <span
              className="sr-only"
              data-turnstile-sentinel
              onFocus={() => {
                const elements = focusableElements();
                elements[elements.length - 1]?.focus();
              }}
              tabIndex={0}
            />
            <h2
              className="mb-1 text-base font-semibold text-[var(--ink)]"
              id={titleId}
            >
              {t("Human verification")}
            </h2>
            <p
              className="mb-4 text-sm text-[var(--ink-muted)]"
              id={descriptionId}
            >
              {t("Please complete human verification to continue.")}
            </p>
          </>
        ) : null}
        <TurnstileField action={action} onTokenChange={handleToken} resetKey={0} />
        {visible ? (
          <button
            className="mt-4 w-full text-sm font-medium text-[var(--ink-muted)] hover:text-brand"
            onClick={() => settleRef.current(null)}
            type="button"
          >
            {t("Cancel")}
          </button>
        ) : null}
        {visible ? (
          <span
            className="sr-only"
            data-turnstile-sentinel
            onFocus={() => focusableElements()[0]?.focus()}
            tabIndex={0}
          />
        ) : null}
      </div>
    </div>
  );
}

/**
 * Renders a Turnstile challenge and resolves with its single-use token, to be
 * sent as the X-Turnstile-Token header on a guarded write.
 *
 * Resolves with null when the user dismisses the challenge, so callers can bail
 * out with `if (!token) return;` rather than handle a rejection.
 */
export function cancelTurnstile() {
  cancelActiveTurnstile?.();
}

export function requireTurnstile(
  action: string,
  signal?: AbortSignal
): Promise<string | null> {
  if (signal?.aborted) return Promise.resolve(null);
  cancelTurnstile();

  return new Promise((resolve) => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);

    let settled = false;
    const settle = (token: string | null) => {
      if (settled) return;
      settled = true;
      window.removeEventListener("pagehide", cancel);
      window.removeEventListener("popstate", cancel);
      signal?.removeEventListener("abort", cancel);
      if (cancelActiveTurnstile === cancel) cancelActiveTurnstile = undefined;
      resolve(token);
      // Unmounting from inside a React callback throws; defer past the commit.
      globalThis.setTimeout(() => {
        root.unmount();
        host.remove();
      }, 0);
    };
    const cancel = () => settle(null);

    cancelActiveTurnstile = cancel;
    window.addEventListener("pagehide", cancel, { once: true });
    window.addEventListener("popstate", cancel, { once: true });
    signal?.addEventListener("abort", cancel, { once: true });

    root.render(<TurnstileGate action={action} onSettle={settle} />);
  });
}
