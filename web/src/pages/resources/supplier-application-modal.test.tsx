// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  createTicket: vi.fn(),
  requireTurnstile: vi.fn(),
  toastSuccess: vi.fn(),
  translate: (key: string) => key,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: mocks.translate }),
}));

vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, disabled, onClick }: any) => (
    <button disabled={disabled} onClick={onClick} type="button">
      {children}
    </button>
  ),
  Modal: ({ children, footer, title, visible }: any) =>
    visible ? (
      <section aria-label={title} role="dialog">
        {children}
        {footer}
      </section>
    ) : null,
  Space: ({ children }: any) => <div>{children}</div>,
  TextArea: ({ onChange, placeholder, value }: any) => (
    <textarea
      onChange={(event) => onChange(event.target.value)}
      placeholder={placeholder}
      value={value}
    />
  ),
  Toast: { error: vi.fn(), success: mocks.toastSuccess, warning: vi.fn() },
}));

vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback,
}));

vi.mock("../tickets/tickets-api", () => ({
  createTicket: mocks.createTicket,
}));

vi.mock("@/components/auth/TurnstileGate", () => ({
  requireTurnstile: mocks.requireTurnstile,
}));

import {
  ensureSupplierRole,
  SupplierApplicationModal,
} from "./supplier-application-modal";

describe("SupplierApplicationModal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.createTicket.mockResolvedValue({});
    mocks.requireTurnstile.mockResolvedValue("turnstile-token");
  });

  afterEach(() => cleanup());

  it("opens the application when the refreshed user still lacks supplier access", async () => {
    const openApplication = vi.fn();

    await expect(
      ensureSupplierRole(
        "user",
        vi.fn().mockResolvedValue({ role: "user" }),
        openApplication
      )
    ).resolves.toBe(false);
    expect(openApplication).toHaveBeenCalledOnce();
  });

  it("creates a general supplier application ticket", async () => {
    const onOpenChange = vi.fn();
    const onSuccess = vi.fn();
    render(
      <SupplierApplicationModal
        open
        onOpenChange={onOpenChange}
        onSuccess={onSuccess}
      />
    );

    fireEvent.change(screen.getByPlaceholderText("Supplier application reason placeholder"), {
      target: { value: "  My reason  " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(mocks.createTicket).toHaveBeenCalledWith(
        {
          ticketType: "general",
          title: "供应商申请",
          firstMessage: "My reason",
        },
        "turnstile-token"
      )
    );
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(onSuccess).toHaveBeenCalledOnce();
  });
});
