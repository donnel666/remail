// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ImportDomainModal } from "./domain-import-modal";

const mocks = vi.hoisted(() => ({
  createDomainResource: vi.fn(),
  onOpenChange: vi.fn(),
  onSuccess: vi.fn(),
  requireTurnstile: vi.fn(),
  resolveTurnstile: undefined as ((token: string | null) => void) | undefined,
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/components/auth/TurnstileGate", () => ({
  requireTurnstile: mocks.requireTurnstile,
}));

vi.mock("@/lib/resources-api", () => ({
  createDomainResource: mocks.createDomainResource,
}));

vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, disabled, onClick }: any) => (
    <button disabled={disabled} onClick={onClick} type="button">
      {children}
    </button>
  ),
  Input: ({ onChange, placeholder, value }: any) => (
    <input
      onChange={(event) => onChange(event.target.value)}
      placeholder={placeholder}
      value={value}
    />
  ),
  Modal: ({ children, footer, onCancel, title, visible }: any) =>
    visible ? (
      <section aria-label={title} role="dialog">
        <button onClick={onCancel} type="button">
          close modal
        </button>
        {children}
        {footer}
      </section>
    ) : null,
  Space: ({ children }: any) => <div>{children}</div>,
  Toast: {
    error: mocks.toastError,
    success: mocks.toastSuccess,
  },
  Typography: {
    Text: ({ children, className }: any) => (
      <span className={className}>{children}</span>
    ),
  },
}));

beforeEach(() => {
  vi.clearAllMocks();
  mocks.resolveTurnstile = undefined;
  mocks.createDomainResource.mockResolvedValue({ id: 1 });
  mocks.requireTurnstile.mockImplementation(
    () =>
      new Promise((resolve) => {
        mocks.resolveTurnstile = resolve;
      })
  );
});

afterEach(cleanup);

describe("ImportDomainModal", () => {
  it("keeps one challenge in flight and does not submit after closing", async () => {
    render(
      <ImportDomainModal
        onOpenChange={mocks.onOpenChange}
        onSuccess={mocks.onSuccess}
        open
      />
    );
    fireEvent.change(screen.getByPlaceholderText("example.com"), {
      target: { value: "Example.com." },
    });
    const submit = screen.getByRole("button", { name: "Import" });

    act(() => {
      submit.click();
      submit.click();
    });
    expect(mocks.requireTurnstile).toHaveBeenCalledTimes(1);
    const signal = mocks.requireTurnstile.mock.calls[0][1] as AbortSignal;

    fireEvent.click(screen.getByRole("button", { name: "close modal" }));
    expect(signal.aborted).toBe(true);
    act(() => mocks.resolveTurnstile?.("verified-token"));

    await waitFor(() => expect(mocks.onOpenChange).toHaveBeenCalledWith(false));
    expect(mocks.createDomainResource).not.toHaveBeenCalled();
    expect(mocks.onSuccess).not.toHaveBeenCalled();
  });

  it("submits the normalized domain with the challenge token", async () => {
    mocks.requireTurnstile.mockResolvedValue("verified-token");
    render(
      <ImportDomainModal
        onOpenChange={mocks.onOpenChange}
        onSuccess={mocks.onSuccess}
        open
      />
    );
    fireEvent.change(screen.getByPlaceholderText("example.com"), {
      target: { value: "Example.com." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() =>
      expect(mocks.createDomainResource).toHaveBeenCalledWith(
        { domain: "example.com" },
        "verified-token",
        expect.any(AbortSignal)
      )
    );
    expect(mocks.toastSuccess).toHaveBeenCalledWith(
      "Domain submitted for verification"
    );
    expect(mocks.onSuccess).toHaveBeenCalledTimes(1);
  });
});
