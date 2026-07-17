// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SendCodeField } from "./SendCodeField";
import { IamApiError } from "@/lib/iam-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/hooks/use-captcha", () => ({
  useCaptcha: () => ({
    captcha: { captchaId: "cap-1", image: "img" },
    loading: false,
    error: null,
    refresh: vi.fn(),
  }),
}));

vi.mock("@/components/auth/CaptchaField", () => ({
  CaptchaField: ({
    value,
    onChange,
  }: {
    value: string;
    onChange: (v: string) => void;
  }) => (
    <input
      aria-label="captcha"
      value={value}
      onChange={(event) => onChange(event.target.value)}
    />
  ),
}));

afterEach(() => cleanup());

type Send = (payload: {
  email: string;
  captchaId: string;
  captchaAnswer: string;
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
      onNotice={onNotice}
      onError={onError}
    />
  );
  fireEvent.change(screen.getByLabelText("captcha"), {
    target: { value: "1234" },
  });
  return { onNotice, onError };
}

// Countdown value shown on the send button, or null when it reads "Send code".
function countdown() {
  const text = (screen.getByRole("button") as HTMLButtonElement).textContent ?? "";
  const match = text.match(/^(\d+)s$/);
  return match ? Number(match[1]) : null;
}

describe("SendCodeField", () => {
  it("starts a resend countdown after a successful send", async () => {
    const send = vi.fn<Send>().mockResolvedValue(undefined);
    const { onNotice } = renderField(send);

    fireEvent.click(screen.getByRole("button"));

    await waitFor(() => expect(countdown()).not.toBeNull());
    // Default 60s cooldown; tolerate one tick to avoid a timer race.
    expect(countdown()).toBeGreaterThanOrEqual(59);
    expect((screen.getByRole("button") as HTMLButtonElement).disabled).toBe(true);
    expect(onNotice).toHaveBeenCalledWith("Verification code sent.");
  });

  it("seeds the countdown from the server Retry-After when throttled", async () => {
    const response = {
      headers: { get: (name: string) => (name === "Retry-After" ? "45" : null) },
    } as unknown as Response;
    const throttled = new IamApiError(429, { message: "slow down" }, response);
    const send = vi.fn<Send>().mockRejectedValue(throttled);
    const { onError } = renderField(send);

    fireEvent.click(screen.getByRole("button"));

    await waitFor(() => expect(countdown()).not.toBeNull());
    // Reflects the server's 45s, not the default 60s.
    const value = countdown();
    expect(value).toBeLessThanOrEqual(45);
    expect(value).toBeGreaterThanOrEqual(44);
    expect((screen.getByRole("button") as HTMLButtonElement).disabled).toBe(true);
    expect(onError).toHaveBeenCalled();
  });
});
