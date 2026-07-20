// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SendCodeField } from "./SendCodeField";
import { IamApiError } from "@/lib/iam-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/components/auth/TurnstileField", () => ({
  TurnstileField: ({
    onTokenChange,
  }: {
    onTokenChange: (token: string) => void;
  }) => (
    <button type="button" aria-label="complete turnstile" onClick={() => onTokenChange("token-1")} />
  ),
}));

afterEach(() => cleanup());

type Send = (payload: {
  email: string;
  turnstileToken: string;
}) => Promise<unknown>;

function renderField(send: Send) {
  const onNotice = vi.fn();
  const onError = vi.fn();
  render(
    <SendCodeField
      email="user@test.com"
      code=""
      onCodeChange={vi.fn()}
      send={send}
      turnstileAction="register_email_code"
      onNotice={onNotice}
      onError={onError}
    />
  );
  fireEvent.click(screen.getByLabelText("complete turnstile"));
  return { onNotice, onError };
}

// Countdown value shown on the send button, or null when it reads "Send code".
function countdown() {
  const text = (screen.getByRole("button", { name: /Send code|\d+s/ }) as HTMLButtonElement).textContent ?? "";
  const match = text.match(/^(\d+)s$/);
  return match ? Number(match[1]) : null;
}

describe("SendCodeField", () => {
  it("starts a resend countdown after a successful send", async () => {
    const send = vi.fn<Send>().mockResolvedValue(undefined);
    const { onNotice } = renderField(send);

    fireEvent.click(screen.getByRole("button", { name: "Send code" }));

    await waitFor(() => expect(countdown()).not.toBeNull());
    // Default 60s cooldown; tolerate one tick to avoid a timer race.
    expect(countdown()).toBeGreaterThanOrEqual(59);
    expect((screen.getByRole("button", { name: /\d+s/ }) as HTMLButtonElement).disabled).toBe(true);
    expect(onNotice).toHaveBeenCalledWith("Verification code sent.");
  });

  it("seeds the countdown from the server Retry-After when throttled", async () => {
    const response = {
      headers: { get: (name: string) => (name === "Retry-After" ? "45" : null) },
    } as unknown as Response;
    const throttled = new IamApiError(429, { message: "slow down" }, response);
    const send = vi.fn<Send>().mockRejectedValue(throttled);
    const { onError } = renderField(send);

    fireEvent.click(screen.getByRole("button", { name: "Send code" }));

    await waitFor(() => expect(countdown()).not.toBeNull());
    // Reflects the server's 45s, not the default 60s.
    const value = countdown();
    expect(value).toBeLessThanOrEqual(45);
    expect(value).toBeGreaterThanOrEqual(44);
    expect((screen.getByRole("button", { name: /\d+s/ }) as HTMLButtonElement).disabled).toBe(true);
    expect(onError).toHaveBeenCalled();
  });

  // Reproduces the reported flow: send → 60s countdown → wait it out → solve
  // Turnstile again → send again. The countdown must restart.
  it("restarts the countdown on a second successful send", async () => {
    vi.useFakeTimers();
    try {
      const send = vi.fn<Send>().mockResolvedValue(undefined);
      renderField(send);

      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Send code" }));
      });
      expect(countdown()).toBe(60);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(60_000);
      });
      expect(countdown()).toBeNull(); // back to "Send code"

      // The finally block consumed the single-use token; the user verifies again.
      fireEvent.click(screen.getByLabelText("complete turnstile"));

      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Send code" }));
      });
      expect(countdown()).toBe(60);
      expect(send).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
